#!/usr/bin/env python3
"""Read-only envelope qualification of a disposable published-source WOW Go daemon.

No operator request, repository claim, workflow dispatch, host control, or
external network: all probes target loopback of the ephemeral native GHA job.
This *does not* qualify effects/claims after crash or deployed Pandora runtime.
"""
import concurrent.futures
import json
import os
from pathlib import Path
import statistics
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:18081"
ENDPOINTS = ("/healthz", "/readyz", "/v1/card")


def cli(*args, timeout=35, check=True):
    return subprocess.run(args, capture_output=True, text=True, timeout=timeout, check=check)


def get(path):
    start = time.monotonic()
    try:
        with urllib.request.urlopen(BASE + path, timeout=4) as r:
            value = json.loads(r.read(65536))
            return {
                "ok": r.status == 200 and isinstance(value, dict),
                "status": r.status, "json": value,
                "ms": round((time.monotonic() - start) * 1000, 3)
            }
    except (OSError, ValueError, urllib.error.URLError) as exc:
        return {"ok": False, "ms": round((time.monotonic() - start) * 1000, 3),
                "type": type(exc).__name__}


def wait_readiness(expected_gen, timeout=20):
    start = time.monotonic()
    while time.monotonic() - start < timeout:
        h, r, c = [get(path) for path in ENDPOINTS]
        if (h["ok"] and r["ok"] and c["ok"]
                and h["json"].get("status") == "ok"
                and r["json"].get("status") == "ready"
                and c["json"].get("schema") == "wow-sidecar.embodiment-card.v1"
                and c["json"].get("generation") == expected_gen):
            return round((time.monotonic() - start)*1000, 3)
        time.sleep(.1)
    raise RuntimeError("timeout waiting for authenticated-by-locality read-only identity/status")


def sample(n, total, expected_gen):
    started = time.monotonic()
    def once(i):
        which = i % 3
        result = get(ENDPOINTS[which])
        body = result.get("json", {})
        valid = result["ok"] and (
            (which == 0 and body.get("status") == "ok")
            or (which == 1 and body.get("status") == "ready")
            or (which == 2 and body.get("schema") == "wow-sidecar.embodiment-card.v1"
                and body.get("generation") == expected_gen))
        return valid, result["ms"], result.get("type")
    with concurrent.futures.ThreadPoolExecutor(max_workers=n) as pool:
        result = list(pool.map(once, range(total)))
    elapsed = time.monotonic() - started
    latencies = sorted(x[1] for x in result)
    return {
        "concurrency": n, "requests": total,
        "successful_requests": sum(x[0] for x in result),
        "non_successes": [dict(index=i, error=x[2]) for i, x in enumerate(result) if not x[0]][:8],
        "p50_ms": round(statistics.median(latencies), 3),
        "p95_observed_ms": latencies[min(len(latencies)-1, int(.95*(len(latencies)-1)))],
        "max_ms": latencies[-1],
        "throughput_requests_per_sec": round(total/max(.000001, elapsed),2),
        "elapsed_ms": round(elapsed*1000, 3),
    }


def proc_rss_kib(name):
    pid = int(cli("docker", "inspect", "-f", "{{.State.Pid}}", name).stdout.strip())
    if pid <= 1:
        return None
    status = Path("/proc") / str(pid) / "status"
    for line in status.read_text().splitlines():
        if line.startswith("VmRSS:"):
            return int(line.split()[1])
    return None


def main(image, arch):
    report={"schema":"wow-readonly-envelope/v1","source_kind":"public-current-main-candidate",
            "arch":arch,"no_operator_effects":True,"real_host_reboot":False,
            "native_owner_recovery_qualified":False}
    name = "wow-rdte-envelope-" + arch
    with tempfile.TemporaryDirectory(prefix="wow-native-envelope-") as temp:
        state=Path(temp)/"state"
        state.mkdir(mode=0o700)
        cli("sudo","chown","10001:10001",str(state))
        def stop():
            cli("docker","rm","-f",name,check=False)
        def start():
            stop()
            cli("docker","run","-d","--name",name,"--read-only",
                "--cpus","2","--memory","128m",
                "--mount",f"type=bind,src={state},dst=/var/lib/wow-sidecar",
                "-e","WOW_NODE_ID=rdte-"+arch,
                "-e","WOW_LOCALITY=cloud",
                "-e","WOW_LISTEN_ADDR=0.0.0.0:8080",
                "-p","127.0.0.1:18081:8080",image)
        try:
            start()
            report["first_ready_ms"]=wait_readiness(1)
            report["startup_rss_kib"]=proc_rss_kib(name)
            report["batches"]=[sample(n,96,1) for n in (1,8,32)]
            report["post_load_rss_kib"]=proc_rss_kib(name)
            report["image_bytes"]=int(cli("docker","image","inspect",image,"--format","{{.Size}}").stdout.strip())
            kill=cli("docker","kill","--signal","SIGKILL",name,check=False)
            report["sigkill_exit"]=kill.returncode
            report["cold_replacement_same_state"]=True
            start()
            report["restart_ready_ms"]=wait_readiness(2)
            report["restart_rss_kib"]=proc_rss_kib(name)
            report["post_restart"]=sample(8,96,2)
            report["restart_generation_incremented"]=get("/v1/card").get("json",{}).get("generation")==2
            report["read_only_oracle_pass"]=(
                all(r["successful_requests"]==r["requests"] for r in report["batches"])
                and report["post_restart"]["successful_requests"]==96
                and report["restart_generation_incremented"]
                and kill.returncode == 0)
        finally:
            stop()
            # Restore only this disposable directory to the runner user so
            # TemporaryDirectory can erase a state path owned by uid 10001.
            cli("sudo", "chown", "-R", f"{os.getuid()}:{os.getgid()}",
                str(state), check=False)
    print("WOW_R7_ENVELOPE=" + json.dumps(report, sort_keys=True), flush=True)
    if not report.get("read_only_oracle_pass"):
        sys.exit(3)


if __name__=="__main__":
    if len(sys.argv)!=3:
        sys.exit("usage: wow-native-readonly-envelope.py IMAGE amd64|arm64")
    main(sys.argv[1],sys.argv[2])
