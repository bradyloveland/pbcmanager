#!/usr/bin/env bash
# CI test of updating from the web UI on a real systemd machine:
#   1. install version A with install.sh
#   2. upload and install B; it starts and is confirmed
#   3. roll back to A by hand, then install B again
#   4. install C, which is built not to start; systemd puts B back
# The builds trust a throwaway key made here, not the project's release key.
set -euo pipefail

say() { printf '\n==> %s\n' "$*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

keys="$(go run ./tools/pbcm-sign genkey ci-test)"
pub="$(sed -n 's/^public:  //p' <<<"$keys")"
export PBCM_SIGNING_KEY
PBCM_SIGNING_KEY="$(sed -n 's/^private: //p' <<<"$keys")"
key_flag="-X github.com/bradyloveland/pbcmanager/internal/release.ExtraKey=$pub"

build() { # version out-dir [extra ldflags]
  make dist VERSION="$1" EXTRA_LDFLAGS="$key_flag ${3:-}" >/dev/null || fail "building $1 failed"
  mkdir -p "$(dirname "$2")"
  rm -rf "$2" && mv dist "$2"
}
say "Building three versions"
build 2.0.0-ci.1 builds/a
build 2.0.0-ci.2 builds/b
build 2.0.0-ci.3 builds/c "-X main.brokenOnPurpose=yes"

say "Installing A"
tar -xzf builds/a/pbcm-2.0.0-ci.1-linux-amd64.tar.gz -C builds/a
sudo builds/a/pbcm-2.0.0-ci.1-linux-amd64/install.sh --skip-client --no-tls >/dev/null
base=http://127.0.0.1:8099/api
jar="$(mktemp)"
api() { curl -fsS -b "$jar" -c "$jar" -H 'X-PBCM: 1' "$@"; }
code="$(sudo pbcm setup-code)"
api -H 'Content-Type: application/json' "$base/setup" \
  -d "{\"code\":\"$code\",\"username\":\"admin\",\"password\":\"ci-password-123\"}" >/dev/null

running() { curl -fsS "$base/health" 2>/dev/null | grep -o '"version":"[^"]*"' | cut -d'"' -f4; }
wait_for() { # description seconds command...
  local what="$1" secs="$2"; shift 2
  for _ in $(seq "$secs"); do "$@" && return 0; sleep 1; done
  fail "timed out waiting for $what"
}
is_version() { [[ "$(running)" == "$1" ]]; }
phase_is() { api "$base/update" | grep -q "\"phase\":\"$1\""; }

upload_install() { # archive version
  api -H 'Content-Type: application/octet-stream' --data-binary @"$1" "$base/update/upload" | grep -q "\"version\":\"$2\"" \
    || fail "upload of $2 wasn't accepted"
  api -X POST "$base/update/install" | grep -q '"restarting":true' || fail "install of $2 didn't start"
}

say "Updating to B"
upload_install builds/b/pbcm-2.0.0-ci.2-linux-amd64.tar.gz 2.0.0-ci.2
wait_for "B to run" 60 is_version 2.0.0-ci.2
wait_for "B to be confirmed" 60 phase_is "done"
test "$(sudo /opt/pbcm/pbcm.prev version)" = 2.0.0-ci.1 || fail "A should be kept as pbcm.prev"

say "Rolling back to A by hand"
api -X POST "$base/update/rollback" | grep -q '"restarting":true' || fail "rollback didn't start"
wait_for "A to run again" 60 is_version 2.0.0-ci.1
phase_is rolled_back || fail "the rollback should be recorded"

say "Updating to B again"
upload_install builds/b/pbcm-2.0.0-ci.2-linux-amd64.tar.gz 2.0.0-ci.2
wait_for "B to run" 60 is_version 2.0.0-ci.2
wait_for "B to be confirmed" 60 phase_is "done"

say "Installing C, which doesn't start"
upload_install builds/c/pbcm-2.0.0-ci.3-linux-amd64.tar.gz 2.0.0-ci.3
wait_for "B to be put back" 120 phase_is rolled_back
wait_for "B to run" 60 is_version 2.0.0-ci.2
api "$base/update" | grep -q "didn't start, on purpose" || fail "the rollback should say why"
systemctl is-active --quiet pbcm || fail "the service should be running"

say "Unsigned releases are refused"
env -u PBCM_SIGNING_KEY make dist VERSION=2.0.0-ci.4 >/dev/null 2>&1
if api -H 'Content-Type: application/octet-stream' --data-binary @dist/pbcm-2.0.0-ci.4-linux-amd64.tar.gz "$base/update/upload" 2>/dev/null; then
  fail "an unsigned release was accepted"
fi
say "All update checks passed"
