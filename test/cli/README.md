# `ao` CLI end-to-end tests

These tests install and drive the **real `ao` binary** the way a user would —
`start` → `status` → `doctor` → `stop`, plus the daemon-control HTTP surface —
and assert the whole thing works end to end. They run against **isolated,
throwaway state** (their own temp run-file + data dir + a free loopback port),
so they never touch a developer's real AO installation.

## Files

| File | Purpose |
|------|---------|
| `smoke.sh` | The suite. Host-agnostic bash; drives the binary at `$AO_BIN` (default `ao` on PATH) and prints a PASS/FAIL line per assertion. |
| `Dockerfile` | Models **installing `ao` on a fresh machine**: builds the binary, drops it on `PATH` in a clean Debian image with only runtime deps (`git`, `tmux`, `curl`), then runs `smoke.sh` as a non-root user. |
| `run-local.sh` | Convenience wrapper: build from source and run `smoke.sh` natively against a temp binary. |

## Run it

**Native (fastest, uses your toolchain):**
```bash
test/cli/run-local.sh
# or, against a binary you already built:
AO_BIN=/path/to/ao test/cli/smoke.sh
```

**Fresh-machine install, in a clean container:**
```bash
docker build -f test/cli/Dockerfile -t ao-cli-smoke .
docker run --rm --init ao-cli-smoke
```
> `--init` is important: it gives the container a real PID-1 reaper so the
> "stale daemon" assertion is reliable. Without it an orphaned daemon can linger
> as a zombie and skew the check.

## What it covers

Install resolves on PATH · `version`/`--version` · `--help` (and hides the
internal `daemon` command) · `doctor` text + `--json` (and that it **does not**
open/migrate SQLite) · `status` stopped/stale/ready · `start` (fresh +
idempotent) · daemon-created store · `/healthz` identity · the `/shutdown`
CSRF/DNS-rebinding guard (403 + daemon survives) · `stop` (graceful + stale +
idempotent) · run-file cleanup/ownership · exit codes (`2` usage, `1` runtime) ·
completion for all four shells.

## Testing strategy — why it's shaped this way

We deliberately don't make Docker the *only* tier. A daemon that detaches with
`setsid` and outlives the launching process is exactly the workload that
container PID-1 semantics mishandle, and the OS-specific bits (`setsid` vs
Windows `CREATE_NEW_PROCESS_GROUP`, and `os.UserConfigDir()` resolving to
`~/Library/Application Support` on macOS, `%AppData%` on Windows, `~/.config`
on Linux) can't be observed from a Linux container at all.

So CI (`.github/workflows/cli-e2e.yml`) runs two tiers:

1. **`native`** — the primary signal. Builds and runs the real binary on a
   GitHub matrix of `ubuntu-latest` + `macos-latest` (those runners *are* the
   VMs), covering the unix detach path and macOS config-dir resolution.
2. **`container`** — a hardening tier. The `Dockerfile` proves a clean-machine
   install works and that the CLI has no hidden dependence on developer state,
   run with `--init`.

### Extending

- Add an assertion: drop a `step`/`assert_*` pair into the relevant section of
  `smoke.sh`. The helpers (`assert_eq`, `assert_contains`, `assert_not_contains`,
  `run_rc`) keep cases one-liners.
- Cover Windows: add a `windows-latest` leg to the `native` matrix (Git Bash
  ships on the runner) once the suite is confirmed green there, or add Go-based
  `os/exec` E2E tests for the Windows process-group path.
- Deeper per-OS path assertions (that state resolves under the OS-native config
  dir when `AO_RUN_FILE`/`AO_DATA_DIR` are unset) are best added as Go unit
  tests in `internal/config`.
