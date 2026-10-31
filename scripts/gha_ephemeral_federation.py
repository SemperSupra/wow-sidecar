#!/usr/bin/env python3
"""Exercise the existing WOW daemon in a disposable, same-host federation."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid

class RepError(Exception):
    pass

def child_env(node, port, state, peer=None, locality='cloud'):
    env = {'PATH': '/usr/bin:/bin', 'LANG': 'C', 'WOW_NODE_ID': node,
           'WOW_LOCALITY': locality, 'WOW_LISTEN_ADDR': f'127.0.0.1:{port}',
           'WOW_GENERATION_STATE_FILE': str(state), 'WOW_LEASE_SECONDS': '15',
           'WOW_PEER_POLL_SECONDS': '5'}
    if peer is not None:
        env['WOW_PEER_URLS'] = f'http://127.0.0.1:{peer}'
    return env

def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]

def get(port, path):
    # Loopback only and no proxies inherited from the caller.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(f'http://127.0.0.1:{port}{path}', timeout=2) as response:
        data = response.read(65537)
        if len(data) > 65536:
            raise RepError('response_too_large')
        return json.loads(data)

def wait_for(procs, port, path, predicate, stage):
    end = time.monotonic() + 35
    while time.monotonic() < end:
        if any(proc.poll() is not None for proc in procs):
            raise RepError(stage + '_process_exited')
        try:
            value = get(port, path)
            if predicate(value):
                return value
        except (OSError, ValueError, urllib.error.URLError):
            pass
        time.sleep(.25)
    raise RepError(stage + '_timeout')

def valid_card(card, node, health, revision, locality='cloud'):
    return (card.get('schema') == 'wow-sidecar.embodiment-card.v1'
            and card.get('protocol') == 'wow-embodiment/v1'
            and card.get('node_id') == node and card.get('locality') == locality
            and card.get('generation') == health.get('generation')
            and card.get('incarnation_id') == health.get('incarnation_id')
            and health.get('node_id') == node and health.get('status') == 'ok'
            and health.get('source_revision') == revision
            and type(card.get('generation')) is int and card['generation'] > 0
            and isinstance(card.get('incarnation_id'), str)
            and re.fullmatch(r'[0-9a-f]{32}', card['incarnation_id']) is not None
            and card.get('endpoints') == [] and card.get('capabilities') == ['system.identity'])

def peer_is(value, node, status, generation=None, incarnation=None):
    peers = value.get('peers', [])
    if (value.get('configured_peers') != 1 or len(peers) != 1
            or (status == 'OBSERVED' and value.get('observed_peers') != 1)):
        return False
    p = peers[0]
    return (p.get('node_id') == node and p.get('status') == status
            and (generation is None or p.get('generation') == generation)
            and (incarnation is None or p.get('incarnation_id') == incarnation)
            and (status != 'OBSERVED' or (p.get('reachability') == 'REACHABLE' and p.get('presence') == 'ONLINE'))
            and (status != 'FETCH_FAILED' or (p.get('reachability') == 'UNREACHABLE'
                 and p.get('presence') in ('DEGRADED', 'OFFLINE'))))

def stop(proc):
    if proc is None:
        return
    if proc.poll() is not None:
        raise RepError('cleanup_process_already_exited')
    proc.send_signal(signal.SIGTERM)
    try:
        proc.wait(timeout=12)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=5)
        raise RepError('cleanup_forced_kill')
    if proc.returncode != 0:
        raise RepError('cleanup_nonzero_exit')

def run(binary, revision, receipt):
    result = {'schema': 'wow-sidecar.ephemeral-federation-receipt.v1',
              'source_revision': revision if re.fullmatch(r'[0-9a-f]{40}', revision) else None,
              'scope': 'same-runner-read-only',
              'status': 'FAIL', 'stage': 'setup', 'first_failure': None,
              'cleanup': 'NOT_STARTED', 'remote_peer_join': False,
              'mutation_exercised': False}
    root = None
    procs = []
    try:
        if not re.fullmatch(r'[0-9a-f]{40}', revision):
            raise RepError('invalid_source_revision')
        if receipt.exists():
            raise RepError('receipt_already_exists')
        if not binary.is_file():
            raise RepError('binary_missing')
        result['binary_sha256'] = hashlib.sha256(binary.read_bytes()).hexdigest()
        version = json.loads(subprocess.check_output([str(binary), 'version'], env={'PATH': '/usr/bin:/bin'}, timeout=10))
        if version.get('source_revision') != revision or version.get('product') != 'wow-sidecar':
            raise RepError('binary_revision_mismatch')
        for key in ('GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT'):
            value = os.environ.get(key, '')
            if value.isascii() and value.isdecimal() and len(value) <= 20:
                result[key.lower()] = value
        root = Path(tempfile.mkdtemp(prefix='wow-ephemeral-'))
        token = uuid.uuid4().hex[:12]
        a, b = 'ci-observer-' + token, 'ci-peer-' + token
        ap, bp = free_port(), free_port()
        while bp == ap:
            bp = free_port()
        def start(node, port, peer=None, locality='cloud'):
            state = root / node
            state.mkdir(exist_ok=True)
            p = subprocess.Popen([str(binary)], env=child_env(node, port, state / 'generation.json', peer, locality),
                                 stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            procs.append(p)
            return p
        result['stage'] = 'launch'
        observer = start(a, ap, bp, 'sovereign')
        peer = start(b, bp, ap)
        ah = wait_for(procs, ap, '/healthz', lambda x: x.get('node_id') == a, 'observer_health')
        bh = wait_for(procs, bp, '/healthz', lambda x: x.get('node_id') == b, 'peer_health')
        if (ah.get('generation') != 1 or bh.get('generation') != 1
                or not valid_card(get(ap, '/v1/card'), a, ah, revision, 'sovereign')
                or not valid_card(get(bp, '/v1/card'), b, bh, revision)):
            raise RepError('card_health_identity_mismatch')
        result['stage'] = 'observe_initial'
        wait_for(procs, ap, '/v1/peers', lambda x: peer_is(x,b,'OBSERVED',bh['generation'],bh['incarnation_id']), 'observe_initial')
        wait_for(procs, bp, '/v1/peers', lambda x: peer_is(x,a,'OBSERVED',ah['generation'],ah['incarnation_id']), 'observe_reverse')
        result['initial'] = {'generation': bh['generation'], 'incarnation_id': bh['incarnation_id'], 'status': 'OBSERVED'}
        result['reverse_initial'] = {'generation': ah['generation'], 'incarnation_id': ah['incarnation_id'], 'status': 'OBSERVED'}
        result['stage'] = 'partition'
        stop(peer)
        procs.remove(peer)
        wait_for(procs, ap, '/v1/peers', lambda x: peer_is(x,b,'FETCH_FAILED',bh['generation'],bh['incarnation_id']), 'partition')
        result['partition'] = 'FETCH_FAILED'
        result['stage'] = 'rejoin'
        peer = start(b, bp, ap)
        nh = wait_for(procs, bp, '/healthz', lambda x: x.get('node_id') == b and x.get('generation', 0) > bh['generation'], 'rejoin_health')
        if (nh.get('generation') != bh['generation'] + 1
                or nh.get('incarnation_id') == bh['incarnation_id']
                or not valid_card(get(bp, '/v1/card'), b, nh, revision)):
            raise RepError('rejoin_identity_mismatch')
        wait_for(procs, ap, '/v1/peers', lambda x: peer_is(x,b,'OBSERVED',nh['generation'],nh['incarnation_id']), 'rejoin')
        wait_for(procs, bp, '/v1/peers', lambda x: peer_is(x,a,'OBSERVED',ah['generation'],ah['incarnation_id']), 'rejoin_reverse')
        result['rejoined'] = {'generation': nh['generation'], 'incarnation_id': nh['incarnation_id'], 'status': 'OBSERVED'}
        result['reverse_rejoined'] = 'OBSERVED'
        result['stage'] = 'cleanup'
    except Exception as exc:
        result['first_failure'] = str(exc) if isinstance(exc, RepError) else type(exc).__name__
    finally:
        errors = []
        for proc in reversed(procs):
            try:
                stop(proc)
            except Exception as exc:
                errors.append(str(exc) if isinstance(exc, RepError) else type(exc).__name__)
        survivors = [proc for proc in procs if proc.poll() is None]
        result['remaining_processes'] = len(survivors)
        if survivors:
            errors.append('child_processes_survived')
            # Keep only process-local cleanup details out of the uploaded receipt.
            if root is not None:
                try:
                    (root / 'survivor-pids').write_text(''.join(str(p.pid) + '\n' for p in survivors))
                except OSError:
                    errors.append('survivor_pid_record_failed')
        if root is not None and not survivors:
            try:
                shutil.rmtree(root)
                if root.exists():
                    errors.append('workcell_remains')
            except OSError:
                errors.append('workcell_removal_failed')
        result['cleanup'] = 'FAIL' if errors else 'PASS'
        if errors:
            result['cleanup_errors'] = errors
            if result['first_failure'] is None:
                result['first_failure'] = errors[0]
        if result['first_failure'] is None:
            result['status'] = 'PASS'
            result['stage'] = 'complete'
        # Exclusive creation ensures prior evidence is never silently overwritten.
        try:
            receipt.parent.mkdir(parents=True, exist_ok=True)
            with receipt.open('x', encoding='utf-8') as output:
                json.dump(result, output, sort_keys=True, indent=2)
                output.write('\n')
        except OSError:
            return 2
    return 0 if result['status'] == 'PASS' else 1

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--revision', required=True)
    parser.add_argument('--receipt', type=Path, required=True)
    args = parser.parse_args()
    def interrupted(signum, frame):
        raise RepError('interrupted')
    previous = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGTERM, signal.SIGINT)}
    try:
        return run(args.binary.resolve(), args.revision, args.receipt)
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)

if __name__ == '__main__':
    sys.exit(main())
