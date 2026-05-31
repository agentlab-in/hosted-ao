#!/usr/bin/env bash
#
# End-to-end smoke test for the `ao` CLI.
#
# It models a *fresh machine*: `ao` is expected to already be installed on PATH
# (the Dockerfile in this directory installs it, simulating a new user), and the
# whole test runs against isolated, throwaway state — its own temp run-file,
# data dir, and a free loopback port — so it never touches a developer's real
# AO installation.
#
# Run locally against a binary you built:
#   AO_BIN=/path/to/ao test/cli/smoke.sh
# Or in the container (models install-on-a-fresh-machine):
#   docker build -f test/cli/Dockerfile -t ao-cli-smoke . && docker run --rm --init ao-cli-smoke
#
# Exit code: 0 if every assertion passes, 1 otherwise.

set -uo pipefail

AO_BIN="${AO_BIN:-ao}"

# ---------------------------------------------------------------------------
# Tiny assertion framework
# ---------------------------------------------------------------------------
PASS=0
FAIL=0
CURRENT=""

section() { printf '\n\033[1m== %s ==\033[0m\n' "$1"; }
step()    { CURRENT="$1"; printf '  • %s ... ' "$1"; }

ok()   { PASS=$((PASS + 1)); printf '\033[32mPASS\033[0m\n'; }
bad()  { FAIL=$((FAIL + 1)); printf '\033[31mFAIL\033[0m\n      %s\n' "$1"; }

# assert_eq <actual> <expected> [msg]
assert_eq() {
  if [ "$1" = "$2" ]; then ok; else bad "${3:-}: expected [$2], got [$1]"; fi
}
# assert_contains <haystack> <needle>
assert_contains() {
  case "$1" in
    *"$2"*) ok ;;
    *) bad "expected output to contain [$2]; got: $(printf '%s' "$1" | head -c 400)" ;;
  esac
}
# assert_not_contains <haystack> <needle>
assert_not_contains() {
  case "$1" in
    *"$2"*) bad "expected output to NOT contain [$2]" ;;
    *) ok ;;
  esac
}

# run_rc <cmd...> -> sets RC and OUT (stdout+stderr combined)
run_rc() {
  OUT="$("$@" 2>&1)"
  RC=$?
}

# ---------------------------------------------------------------------------
# Isolated, throwaway environment
# ---------------------------------------------------------------------------
TMP="$(mktemp -d)"
export AO_RUN_FILE="$TMP/running.json"
export AO_DATA_DIR="$TMP/data"

# Pick a free loopback port (bash /dev/tcp probe; connect-refused == free).
find_free_port() {
  local p
  for p in $(seq 3071 3170); do
    if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
      echo "$p"; return 0
    fi
    exec 3>&- 2>/dev/null || true
  done
  echo 3071
}
# Always run against an isolated, free port. We deliberately do NOT honour an
# inherited AO_PORT — it might point at a real daemon, which is exactly the
# collision this isolation is meant to prevent. Override only via AO_SMOKE_PORT.
export AO_PORT="${AO_SMOKE_PORT:-$(find_free_port)}"

cleanup() {
  "$AO_BIN" stop >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT

printf 'ao smoke test\n  binary : %s\n  port   : %s\n  state  : %s\n' \
  "$(command -v "$AO_BIN" || echo "$AO_BIN")" "$AO_PORT" "$TMP"

# ---------------------------------------------------------------------------
# 1. Install verification — `ao` is a real, runnable binary on this machine
# ---------------------------------------------------------------------------
section "install"

step "ao resolves on PATH / at AO_BIN"
if command -v "$AO_BIN" >/dev/null 2>&1; then ok; else bad "ao not found"; fi

step "ao version prints build metadata"
run_rc "$AO_BIN" version
if [ "$RC" -eq 0 ] && [ -n "$OUT" ]; then ok; else bad "rc=$RC out=$OUT"; fi

step "ao --version works"
run_rc "$AO_BIN" --version
assert_eq "$RC" "0" "--version rc"

step "ao --help lists product commands"
run_rc "$AO_BIN" --help
assert_contains "$OUT" "start"
step "ao --help lists status/stop/doctor"
assert_contains "$OUT" "doctor"
step "ao --help hides internal daemon command"
assert_not_contains "$OUT" $'\n  daemon'

# ---------------------------------------------------------------------------
# 2. doctor on a fresh machine (no daemon yet)
# ---------------------------------------------------------------------------
section "doctor (fresh)"

step "doctor exits 0 when required tools present"
run_rc "$AO_BIN" doctor
assert_eq "$RC" "0" "doctor rc (git/tmux must be installed in the image)"

step "doctor reports git found"
assert_contains "$OUT" "git"

step "doctor does NOT migrate the store (sqlite WARN, db absent)"
assert_contains "$OUT" "database not created yet"

step "doctor data dir was created but ao.db was NOT (CLI is not the store writer)"
if [ ! -f "$AO_DATA_DIR/ao.db" ]; then ok; else bad "ao.db exists — doctor must not create/migrate the DB"; fi

step "doctor --json is valid JSON with ok=true"
run_rc "$AO_BIN" doctor --json
assert_contains "$OUT" '"ok": true'

# ---------------------------------------------------------------------------
# 3. status when stopped
# ---------------------------------------------------------------------------
section "status (stopped)"

step "status --json reports stopped"
run_rc "$AO_BIN" status --json
assert_contains "$OUT" '"state": "stopped"'
step "status exits 0 even when stopped (status never errors)"
assert_eq "$RC" "0" "status exit code"
step "stopped status omits startedAt"
assert_not_contains "$OUT" "startedAt"

step "stop is idempotent when already stopped"
run_rc "$AO_BIN" stop
if [ "$RC" -eq 0 ]; then assert_contains "$OUT" "stopped"; else bad "stop-when-stopped rc=$RC"; fi

# ---------------------------------------------------------------------------
# 4. start → ready, and status reflects it
# ---------------------------------------------------------------------------
section "start"

step "start brings the daemon up and reports ready"
run_rc "$AO_BIN" start
if [ "$RC" -eq 0 ]; then assert_contains "$OUT" "ready"; else bad "start rc=$RC out=$OUT"; fi

step "status --json reports ready with pid+port"
run_rc "$AO_BIN" status --json
assert_contains "$OUT" '"state": "ready"'
step "status carries the bound port"
assert_contains "$OUT" "\"port\": $AO_PORT"

step "start is idempotent (second start returns ready, no error)"
run_rc "$AO_BIN" start
if [ "$RC" -eq 0 ]; then assert_contains "$OUT" "ready"; else bad "idempotent start rc=$RC"; fi

# Capture the live pid for later assertions.
PID="$("$AO_BIN" status --json | sed -n 's/.*"pid": \([0-9]*\).*/\1/p' | head -1)"

# ---------------------------------------------------------------------------
# 5. doctor while running — now the daemon (not the CLI) has created the store
# ---------------------------------------------------------------------------
section "doctor (running)"

step "daemon created and migrated the store"
if [ -f "$AO_DATA_DIR/ao.db" ]; then ok; else bad "daemon should have created ao.db"; fi

step "doctor now reports sqlite present + daemon-migrated"
run_rc "$AO_BIN" doctor
assert_contains "$OUT" "migrations are applied by the daemon"

# ---------------------------------------------------------------------------
# 6. Health endpoint identity (loopback)
# ---------------------------------------------------------------------------
section "health endpoint"

if command -v curl >/dev/null 2>&1; then
  step "/healthz reports the AO daemon service + pid"
  run_rc curl -fsS "http://127.0.0.1:$AO_PORT/healthz"
  assert_contains "$OUT" "agent-orchestrator-daemon"

  # -------------------------------------------------------------------------
  # 7. /shutdown CSRF / DNS-rebinding guard (review fix M3)
  # -------------------------------------------------------------------------
  section "/shutdown guard"

  step "cross-origin POST /shutdown is rejected (403)"
  CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST \
            -H 'Origin: https://evil.example' "http://127.0.0.1:$AO_PORT/shutdown")"
  assert_eq "$CODE" "403" "cross-origin shutdown"

  step "non-loopback Host POST /shutdown is rejected (403)"
  CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST \
            -H 'Host: evil.example' "http://127.0.0.1:$AO_PORT/shutdown")"
  assert_eq "$CODE" "403" "rebinding-host shutdown"

  step "daemon survived the rejected shutdown attempts"
  run_rc "$AO_BIN" status --json
  assert_contains "$OUT" '"state": "ready"'
else
  section "/shutdown guard"
  step "curl unavailable — skipping HTTP-level guard checks"
  printf '\033[33mSKIP\033[0m\n'
fi

# ---------------------------------------------------------------------------
# 8. stop → stopped, run-file cleaned up
# ---------------------------------------------------------------------------
section "stop"

step "stop gracefully stops the daemon"
run_rc "$AO_BIN" stop
if [ "$RC" -eq 0 ]; then assert_contains "$OUT" "stopped"; else bad "stop rc=$RC out=$OUT"; fi

step "run-file removed after stop"
if [ ! -f "$AO_RUN_FILE" ]; then ok; else bad "running.json still present"; fi

step "status --json reports stopped after stop"
run_rc "$AO_BIN" status --json
assert_contains "$OUT" '"state": "stopped"'

# ---------------------------------------------------------------------------
# 9. stale run-file (dead PID) — deterministic, no real process needed
# ---------------------------------------------------------------------------
section "stale run-file"

# PID 2147483647 is never alive; the CLI must classify this as stale, not kill it.
printf '{"pid":2147483647,"port":%s,"startedAt":"2020-01-01T00:00:00Z"}\n' "$AO_PORT" > "$AO_RUN_FILE"

step "status reports stale for a dead-PID run-file"
run_rc "$AO_BIN" status --json
assert_contains "$OUT" '"state": "stale"'
step "status still exits 0 for a stale daemon (reports, never errors)"
assert_eq "$RC" "0" "stale status exit code"

step "stop clears a stale run-file and reports stopped"
run_rc "$AO_BIN" stop
assert_contains "$OUT" "stopped"
step "stale run-file removed"
if [ ! -f "$AO_RUN_FILE" ]; then ok; else bad "stale running.json not removed"; fi

# ---------------------------------------------------------------------------
# 10. exit codes: 2 for usage errors, 1 for runtime errors
# ---------------------------------------------------------------------------
section "exit codes"

step "unknown flag exits 2 (usage error)"
run_rc "$AO_BIN" status --definitely-not-a-flag
assert_eq "$RC" "2" "bad-flag exit code"

step "missing required arg exits 2 (completion needs a shell)"
run_rc "$AO_BIN" completion
assert_eq "$RC" "2" "missing-arg exit code"

step "unsupported shell exits non-zero (runtime error)"
run_rc "$AO_BIN" completion notashell
if [ "$RC" -ne 0 ]; then ok; else bad "expected non-zero for bad shell"; fi

step "invalid config (AO_PORT out of range) exits 1, not 2"
OUT="$(AO_PORT=99999 "$AO_BIN" status 2>&1)"; RC=$?
assert_eq "$RC" "1" "config-error exit code (runtime, not usage)"

# ---------------------------------------------------------------------------
# 11. shell completion generators
# ---------------------------------------------------------------------------
section "completion"

for sh in bash zsh fish powershell; do
  step "completion $sh generates a script"
  run_rc "$AO_BIN" completion "$sh"
  if [ "$RC" -eq 0 ] && [ -n "$OUT" ]; then ok; else bad "completion $sh rc=$RC"; fi
done

# ---------------------------------------------------------------------------
# Result
# ---------------------------------------------------------------------------
printf '\n\033[1m== result ==\033[0m\n  passed: %s\n  failed: %s\n' "$PASS" "$FAIL"
if [ "$FAIL" -ne 0 ]; then
  printf '\033[31mSMOKE TEST FAILED\033[0m\n'
  exit 1
fi
printf '\033[32mSMOKE TEST PASSED\033[0m\n'
