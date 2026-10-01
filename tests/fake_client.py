#!/usr/bin/env python3
"""Stand-in for proxmox-backup-client used by the test suite.

Behavior is chosen from the arguments so tests stay deterministic:
  * a backup whose folder path contains "fail" exits 255 with an error
  * a backup whose folder path contains "slow" runs for 30 seconds
  * PBS_PASSWORD "bad" makes every server call fail with a permission error
  * a snapshot group containing "empty" has no snapshots
If FAKE_PBC_LOG is set, each invocation's arguments and PBS_* environment
are appended to that file as one JSON line.
"""
import json
import os
import sys
import time

args = sys.argv[1:]
if os.environ.get("FAKE_PBC_LOG"):
    with open(os.environ["FAKE_PBC_LOG"], "a") as f:
        env = {k: v for k, v in os.environ.items() if k.startswith("PBS_")}
        f.write(json.dumps({"args": args, "env": env}) + "\n")

cmd = args[0] if args else ""
if cmd == "version":
    print("client version: 4.0.99")
    sys.exit(0)
if os.environ.get("PBS_PASSWORD") == "bad":
    print("Error: permission check failed.", file=sys.stderr)
    sys.exit(1)
if cmd == "status":
    print(json.dumps({"total": 20 * 2**40, "used": 5 * 2**40, "avail": 15 * 2**40}))
    sys.exit(0)
if cmd == "snapshot":
    group = args[2] if len(args) > 2 else ""
    if "empty" in group:
        print("[]")
    else:
        print(json.dumps([
            {"backup-type": "host", "backup-id": group.split("/")[-1], "backup-time": 1790000000,
             "size": 1000, "files": [{"filename": "a.mpxar.didx"}], "verification": {"state": "ok"}},
            {"backup-type": "host", "backup-id": group.split("/")[-1], "backup-time": 1790086400,
             "size": 1234567, "files": [{"filename": "a.mpxar.didx"}], "protected": True},
        ]))
    sys.exit(0)
if cmd == "backup":
    joined = " ".join(args)
    print("Starting backup: host/test")
    print("Client name: fake")
    if "slow" in joined:
        for i in range(300):
            print(f"progress {i}", flush=True)
            time.sleep(0.1)
    if "fail" in joined:
        print("Error: connection reset by peer")
        sys.exit(255)
    print("Duration: 0.01s")
    sys.exit(0)
print(f"Error: unknown command {cmd}", file=sys.stderr)
sys.exit(2)
