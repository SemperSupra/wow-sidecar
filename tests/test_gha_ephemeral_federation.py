import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('rep', Path(__file__).parents[1] / 'scripts/gha_ephemeral_federation.py')
rep = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rep)
REV = 'a' * 40

class EphemeralRepTests(unittest.TestCase):
    def test_child_environment_is_an_allowlist(self):
        with mock.patch.dict(os.environ, {'GITHUB_TOKEN': 'secret', 'AWS_SECRET_ACCESS_KEY': 'secret', 'WOW_CONTROL_REPOSITORY': 'injected', 'HTTPS_PROXY': 'secret'}):
            env = rep.child_env('node', 1234, Path('/tmp/state'), 1235)
        self.assertFalse(set(env) & {'GITHUB_TOKEN','AWS_SECRET_ACCESS_KEY','WOW_CONTROL_REPOSITORY','HTTPS_PROXY'})
        self.assertEqual(env['WOW_PEER_URLS'], 'http://127.0.0.1:1235')
        self.assertEqual(env['WOW_LOCALITY'], 'cloud')

    def test_card_health_and_identity_bound(self):
        card = {'schema':'wow-sidecar.embodiment-card.v1','protocol':'wow-embodiment/v1','node_id':'node','locality':'cloud','generation':1,'incarnation_id':'a'*32,'endpoints':[],'capabilities':['system.identity']}
        health = {'node_id':'node','status':'ok','generation':1,'incarnation_id':'a'*32,'source_revision':REV}
        self.assertTrue(rep.valid_card(card,'node',health,REV))
        for key, value in [('generation',2),('incarnation_id','other'),('node_id','other'),('locality','sovereign'),('endpoints',['x']),('capabilities',['other'])]:
            with self.subTest(key=key):
                bad = dict(card, **{key:value})
                self.assertFalse(rep.valid_card(bad,'node',health,REV))
        self.assertFalse(rep.valid_card(card,'node',dict(health,source_revision='b'*40),REV))

    def test_peer_generation_and_incarnation_are_both_fenced(self):
        peer={'node_id':'peer','generation':2,'incarnation_id':'new','status':'OBSERVED','reachability':'REACHABLE','presence':'ONLINE'}
        view={'configured_peers':1,'observed_peers':1,'peers':[peer]}
        self.assertTrue(rep.peer_is(view,'peer','OBSERVED',2,'new'))
        self.assertFalse(rep.peer_is(view,'peer','OBSERVED',2,'old'))
        self.assertFalse(rep.peer_is(view,'peer','OBSERVED',1,'new'))
        self.assertFalse(rep.peer_is(view,'peer','OBSERVED',2,'newer'))

    def test_partition_requires_degraded_or_offline_presence(self):
        view={'configured_peers':1,'peers':[{'node_id':'peer','generation':1,
              'incarnation_id':'i','status':'FETCH_FAILED','reachability':'UNREACHABLE',
              'presence':'ONLINE'}]}
        self.assertFalse(rep.peer_is(view,'peer','FETCH_FAILED',1,'i'))
        view['peers'][0]['presence']='DEGRADED'
        self.assertTrue(rep.peer_is(view,'peer','FETCH_FAILED',1,'i'))

    def test_missing_binary_produces_failure_receipt(self):
        with tempfile.TemporaryDirectory() as d:
            receipt=Path(d)/'receipt.json'
            self.assertEqual(rep.run(Path(d)/'missing',REV,receipt),1)
            result=json.loads(receipt.read_text())
            self.assertEqual(result['first_failure'],'binary_missing')
            self.assertEqual(result['cleanup'],'PASS')
            self.assertEqual(result['status'],'FAIL')

    def test_receipt_is_never_overwritten(self):
        with tempfile.TemporaryDirectory() as d:
            receipt=Path(d)/'receipt.json';receipt.write_text('original')
            self.assertEqual(rep.run(Path(d)/'missing',REV,receipt),2)
            self.assertEqual(receipt.read_text(),'original')

    def test_early_child_exit_is_a_failure(self):
        proc=mock.Mock();proc.poll.return_value=1
        with self.assertRaisesRegex(rep.RepError,'probe_process_exited'):
            rep.wait_for([proc],1234,'/healthz',lambda x:True,'probe')

    def test_first_failure_survives_cleanup_failure(self):
        with tempfile.TemporaryDirectory() as d:
            receipt=Path(d)/'receipt.json'; binary=Path(d)/'binary';binary.write_bytes(b'x')
            proc=mock.Mock(); proc.poll.return_value=None
            version=json.dumps({'product':'wow-sidecar','source_revision':REV}).encode()
            with mock.patch.object(rep.subprocess,'check_output',return_value=version), \
                 mock.patch.object(rep.subprocess,'Popen',return_value=proc), \
                 mock.patch.object(rep,'wait_for',side_effect=rep.RepError('initial_failure')), \
                 mock.patch.object(rep,'stop',side_effect=rep.RepError('cleanup_failure')), \
                 mock.patch.object(rep.tempfile,'mkdtemp',return_value=d):
                self.assertEqual(rep.run(binary,REV,receipt),1)
            result=json.loads(receipt.read_text())
            self.assertEqual(result['first_failure'],'initial_failure')
            self.assertEqual(result['cleanup'],'FAIL')
            self.assertIn('cleanup_failure',result['cleanup_errors'])

    def test_bad_revision_and_error_are_sanitized(self):
        with tempfile.TemporaryDirectory() as d:
            receipt=Path(d)/'receipt.json'
            self.assertEqual(rep.run(Path(d)/'missing','secret-not-a-sha',receipt),1)
            result=json.loads(receipt.read_text())
            self.assertEqual(result['first_failure'],'invalid_source_revision')
            self.assertNotIn('secret-not-a-sha', receipt.read_text())

if __name__ == '__main__':
    unittest.main()
