# Phase 2: The Installer, Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `curl -fsSL <url> | sh` on a fresh Linux box (Raspberry Pi or cloud VM) downloads the `ao` CLI, installs and starts the daemon and pair-mode gateway, and prints one `ao-pair://` string, once, clearly, ready to paste into the desktop app.

**Architecture:** A tiny POSIX shell script (`install.sh`) that does OS/arch detection, download, checksum verification, and a single privileged exec of the box-side provisioning command Phase 1 lands. All the actual work (systemd units, certificate, passcode, the pairing-string codec and its printing) already exists or is being built in Phase 1; this phase's only real engineering gap is that **no durable, anonymously-downloadable Linux `ao` binary exists on any GitHub Release today** (confirmed by reading `.github/workflows/linux-cli-binaries.yml`, whose own header says "Creates no release and publishes nothing"). Closing that gap is Task 1; the script is Task 2; everything else is verification and hosting plumbing.

**Tech Stack:** POSIX `/bin/sh` (the installer), GitHub Actions (the one authorized publisher, `frontend-release.yml`), Go (`backend/cmd/ao`, unchanged except for what Phase 1 already does to it), Docker (fixture-based CI checks, mirroring `test/cli/`).

**Spec:** `docs/superpowers/specs/2026-08-19-seamless-machine-onboarding-design.md` (section "Box-side flow"). **Depends on:** `docs/superpowers/plans/2026-08-20-phase-1-pairing-string.md` landing first (`ao pair show` and the pairing-string emission at provisioning/rotation time). This plan is written against Phase 1's plan as literally scoped, not against the spec's prose alone; see Task 2 Step 0 for the one place those two documents disagree.

## Global Constraints

- Depends on Phase 1 (`docs/superpowers/plans/2026-08-20-phase-1-pairing-string.md`) having landed on `develop`. Branch from `develop` after that PR merges, PR back to `develop`. Conventional commits, no AI attribution footer, no em or en dashes anywhere (this plan document follows that same rule).
- The pairing string is a credential end to end. `install.sh` must never redirect, `tee`, log, or capture the provisioning command's stdout/stderr to any file, and must never print anything of its own after that command's output that could be mistaken for part of it or obscure it.
- All app state stays under `~/.ao/hosted`, derived from `config.StateRootSegments()` (Go). Phase 2 does not touch state paths directly: `install.sh` never writes into `~/.ao` itself; every path decision (`/usr/local/bin/ao`, the systemd unit files, `~/.ao/hosted/vm-gateway/pair-cert`, `~/.ao/hosted/vm-gateway/pair-passcode`) is made by the existing `ao setup-vm --pair` machinery (`backend/internal/cli/setupvm.go`, `setupvm_plan.go`), which already derives them correctly. This is inherited for free by keeping the script dumb.
- Any new hosted-only SQLite migration numbers would need to be `>= 0200`; none is expected in this phase (no schema touched).
- "Exactly one publisher" (AGENTS.md hard rule): the new Linux CLI binaries are published only from `frontend-release.yml`'s existing `release` environment (the approval-gated job that already publishes the Electron installers), never from a second workflow. `linux-cli-binaries.yml` stays exactly as it is today: a PR/`develop`-push CI signal with a 7-day artifact and no publish step.
- No account or control-plane coupling anywhere in `install.sh`. It downloads a binary and runs one local command. It never talks to `ao.agentlab.in`.
- i18n is not touched by this phase: `install.sh` runs before the desktop app is involved, and its own output is operator-facing shell text, not a renderer string.

## Design decision: how the script gets root

`ao setup-vm --pair`'s preflight (`checkSetupPrivilege` in `backend/internal/cli/setupvm_plan.go`) refuses to run at all unless the process is already UID 0, or `sudo -n true` succeeds (passwordless sudo). It deliberately never prompts for a sudo password mid-install. `install.sh` therefore does not try to be clever about detecting sudo-ability itself: when not already root, it wraps the **entire** provisioning invocation in one plain `sudo` call:

```
sudo "$tmp_dir/ao" setup-vm --pair
```

By the time the Go binary starts, it is already UID 0 (`os.Getuid() == 0`), so the preflight's privilege check passes trivially with no internal `sudo -n` re-exec needed. `sudo` itself opens `/dev/tty` directly for its password prompt when it needs one (standard behavior on every distro this targets), so this works over `curl -fsSL ... | sh` on a real interactive SSH session even though the pipe has already consumed the shell's own stdin. Cloud-init default users (`azureuser` on Azure, `ubuntu` on AWS/GCP, `pi`/the custom user on Raspberry Pi OS) are NOPASSWD-sudo by default, so in practice no prompt appears at all. If sudo genuinely cannot obtain a password (fully non-interactive automation with password-required sudo), the script fails with one clear line pointing at re-running interactively; this is a documented limitation, not a bug to solve here.

## Task 1: Publish durable Linux `ao` CLI binaries on the release

**Files:**
- Modify: `.github/workflows/frontend-release.yml` (the `release` job's `ubuntu-latest` leg, next to the existing "Upload stable asset aliases (Linux)" step)
- Reference, unchanged: `.github/workflows/linux-cli-binaries.yml` (the build/verify logic to mirror, not call)

**Interfaces:**
- Produces, on every real `desktop-v*` release (tag `v<frontend/package.json version>`, exactly like the existing AppImage/exe/zip aliases): release assets `ao-linux-x64`, `ao-linux-x64.sha256`, `ao-linux-arm64`, `ao-linux-arm64.sha256`. Bare, statically-linked (`CGO_ENABLED=0`), version-stamped (`-X .../cli.Version=...`) binaries of `backend/cmd/ao`, distinct from `agent-orchestrator-linux-x64.AppImage` (the full Electron GUI bundle, wrong artifact for a headless box).
- The `.sha256` file's content names the final asset filename (`<hash>  ao-linux-x64`), not the intermediate `dist/ao`, so `sha256sum -c ao-linux-x64.sha256` works when both files are downloaded side by side.

- [ ] **Step 1:** Add a step to the `ubuntu-latest` leg of the `release` job, after the Go toolchain is already set up (it is, for `build-daemon.mjs`), that cross-compiles `linux/amd64` and `linux/arm64` with the same command `linux-cli-binaries.yml` uses (`CGO_ENABLED=0 GOOS=linux GOARCH=<arch> go build -trimpath -ldflags "-X .../cli.Version=... -X .../cli.Commit=... -X .../cli.Date=..." -o dist/ao-<target> ./cmd/ao`), version-stamped from the same `v$(node -p "require('./package.json').version")` this job already resolves for its own tag.
- [ ] **Step 2:** Run the same artifact verification `linux-cli-binaries.yml` already runs (`file` reports the right ELF machine per arch; on amd64 also run `dist/ao-linux-x64 version` and assert the stamp), so a silently-wrong cross-compile fails the release the same way it fails PR CI today.
- [ ] **Step 3:** Generate `<name>.sha256` for each binary referencing the final asset name, and `gh release upload "$tag" ao-linux-x64 ao-linux-x64.sha256 ao-linux-arm64 ao-linux-arm64.sha256 --clobber`, matching the existing macOS/Windows/Linux alias steps' pattern exactly (same job, same `GH_TOKEN`, same `--clobber` idempotency).
- [ ] **Step 4:** Update `frontend/docs/desktop-release.md`'s asset list (it already enumerates `agent-orchestrator-win32-x64.exe`, `agent-orchestrator-linux-x64.AppImage`, etc.) to include the two new CLI assets, so the one canonical asset-list doc stays complete.
- [ ] **Step 5:** Cut a fork test release (per the workflow's own "Cutting a FORK test release" instructions: push a `desktop-v0.0.0-testN` tag) and verify with `gh release view v0.0.0-testN --repo agentlab-in/hosted-ao --json assets --jq '.assets[].name'` that all four new names are present, then `curl -fsSLO .../releases/latest/download/ao-linux-x64 && sha256sum -c ao-linux-x64.sha256 && chmod +x ao-linux-x64 && ./ao-linux-x64 version`.
- [ ] **Step 6:** Commit `feat(release): publish standalone linux ao CLI binaries`.

## Task 2: `install.sh`

**Files:**
- Create: `install.sh` (repo root, so the interim `raw.githubusercontent.com` URL is short and the eventual `get.agentlab.in` swap is a pure hosting change, not a script change)

**Interfaces:**
- `detect_os()` returns `linux` or fails with a clear "not supported yet" message and exit 1 for anything else (Darwin included; see the open question below).
- `detect_arch()` maps `uname -m`: `x86_64` -> `x64`, `aarch64`/`arm64` -> `arm64`; anything else (`armv7l`, `i386`, ...) fails clearly, naming what was detected and that only 64-bit x86 and ARM Linux are published.
- `asset_name(arch)` returns `ao-linux-x64` or `ao-linux-arm64`.
- Download base is `https://github.com/${AO_INSTALL_REPO:-agentlab-in/hosted-ao}/releases/latest/download/<asset>`, the same constant `releases/latest/download` shape `ao start` already relies on (`backend/internal/cli/start.go`'s `downloadURL`), so `AO_INSTALL_REPO` is the one override a fork test loop needs. This is a script-local variable, deliberately not named `AO_RELEASE_REPO`, to avoid confusion with the unrelated Go-internal `cli.releaseRepo` build-time ldflag that governs `ao start`'s own desktop-app fetch.
- On success, the script's last action is one exec of the provisioning command with stdout/stderr fully inherited, untouched, unredirected: **`ao setup-vm --pair`** run as root (directly, or via a single wrapping `sudo`, per the design decision above).

- [ ] **Step 0 (coordination, do first):** Confirm against the landed Phase 1 implementation which invocation is the actual provisioning entry point. Phase 1's plan Task 2 modifies `ao setup-vm --pair`'s *output* (adds the `ao-pair://` string) and adds `ao pair`/`ao pair show` as a new parent/subcommand pair, but does not commit to making bare `ao pair` an alias for provisioning, even though the design spec's prose describes `ao pair` as the thing that "provisions (or reuses)". If Phase 1 ships `ao pair` (no subcommand) as a true alias, use that; otherwise keep `ao setup-vm --pair`. This is a one-line constant in the script either way. See "Open questions for Harshit."
- [ ] **Step 1: Write the failing test fixtures first** (`test/cli/install/`): a small shell assertion script that sources `install.sh` with `AO_INSTALL_SOURCED=1` (so the file's own `main` at the bottom does not auto-run) and exercises `detect_os`/`detect_arch`/`asset_name` under injected `AO_INSTALL_OS_OVERRIDE`/`AO_INSTALL_ARCH_OVERRIDE` test-only env vars, asserting: `x86_64` -> `ao-linux-x64`, `aarch64` -> `ao-linux-arm64`, `armv7l` -> a named failure, `Darwin` -> a named "not supported yet" failure, and that a corrupted download (mismatched sha256 fixture file) is rejected before `chmod +x` ever runs.
- [ ] **Step 2: Run to verify failure** (the fixtures reference functions `install.sh` does not have yet).
- [ ] **Step 3: Implement `install.sh`**: `set -eu` (POSIX, no bashisms, since `sh` is not guaranteed to be bash on a minimal image); detect OS and arch; require `curl` and `sha256sum` (or `shasum -a 256` as a fallback for the "ideally macOS" path if that ships) with a clear message if absent; `mktemp -d`, download the binary and its `.sha256` sidecar, verify, `chmod +x`; determine root vs. sudo-wrap; exec `ao setup-vm --pair` (or the Step 0 outcome) with a `trap 'rm -rf "$tmp_dir"' EXIT` for cleanup, placed so it fires after the provisioning command finishes, not before. No `tee`, no log file, no second echo of anything the provisioning command already prints.
- [ ] **Step 4: Run fixtures to green.**
- [ ] **Step 5:** `shellcheck install.sh` clean (add a CI step for this if none exists at repo root today; check first, since `.github/workflows` has no `shellcheck` job currently).
- [ ] **Step 6: Commit** `feat(install): curl|sh installer for the ao CLI and pair-mode provisioning`.

## Task 3: CI verification for `install.sh`

**Files:**
- Create: `test/cli/install/install-detect-test.sh` (from Task 2 Step 1, promoted into CI)
- Modify: `.github/workflows/cli-e2e.yml` (add a job) or a new small workflow, matching the existing `container` job's shape (`test/cli/Dockerfile`, `install-check.sh`)

**Interfaces:**
- A CI job that runs the Task 2 fixture script on `ubuntu-latest` with no privilege needed (pure function-level assertions, no real download, no systemd).
- A second, explicitly-scoped check that the script's real download path 404s cleanly against a repo with no published assets, mirroring the existing `AgentWrapper/ao-fresh-install-fixture` pattern in `test/cli/Dockerfile`/`install-check.sh`: `AO_INSTALL_REPO=agentlab-in/does-not-exist-fixture ./install.sh` must fail with a clear, single-line error, never a stack trace or a silent success.

- [ ] **Step 1:** Add the job, run `test/cli/install/install-detect-test.sh` directly on the runner (no Docker needed for this half).
- [ ] **Step 2:** Add the 404-fixture check as a second step in the same job.
- [ ] **Step 3:** Run locally first (`sh test/cli/install/install-detect-test.sh`), confirm green, then push and confirm the Actions run is green.
- [ ] **Step 4: Commit** `test(install): fixture checks for os/arch detection and the 404 path`.
- **Deliberately not built here:** a systemd-in-Docker smoke test that actually runs `ao setup-vm --pair` end to end inside CI. That is real infrastructure (privileged containers, cgroup mounts, a systemd-capable base image) for a check the live acceptance run in Task 5 already covers authoritatively, and building it now is exactly the kind of premature machinery the "design for later, build only now" constraint rules out. Revisit only if the vm-shared run surfaces a class of bug this would have caught.

## Task 4: Hosting plumbing and docs

**Files:**
- Modify: `README.md` (the "Add a machine"/getting-started section Phase 1's Task 6 adds or extends)
- Modify: `frontend/docs/desktop-release.md` (already touched in Task 1, add the hosting note)

**Interfaces:**
- The copy-paste command shown to users, for now: `curl -fsSL https://raw.githubusercontent.com/agentlab-in/hosted-ao/develop/install.sh | sh`.
- One inline comment at the top of `install.sh` stating plainly that the script contains no reference to its own fetch URL, so pointing `get.agentlab.in` at this same file later (reverse-proxied or copied to static hosting) requires zero script changes, only a DNS/hosting change and updating the one command shown in docs.

- [ ] **Step 1:** Add the curl command to `README.md` next to the existing paste-a-string desktop flow Phase 1 documents, explicitly labeled as the box-side half.
- [ ] **Step 2:** Add a short "hosting" note to `frontend/docs/desktop-release.md` recording the interim URL, the `get.agentlab.in` plan, and that the swap touches docs only, not the script.
- [ ] **Step 3:** Verify the raw URL actually serves the right content once Task 2 merges to `develop`: `curl -fsSL https://raw.githubusercontent.com/agentlab-in/hosted-ao/develop/install.sh | head -5`.
- [ ] **Step 4: Commit** `docs(install): curl command and interim hosting note`.

## Task 5: End-to-end acceptance on vm-shared

**Target:** `azureuser@20.197.63.75` (Ubuntu, Azure VM, "vm-shared").

- [ ] **Step 1:** SSH in and confirm a clean starting state: `ssh azureuser@20.197.63.75 'command -v ao || echo "no ao on PATH, good"'`.
- [ ] **Step 2:** Run the real installer: `ssh azureuser@20.197.63.75 'curl -fsSL https://raw.githubusercontent.com/agentlab-in/hosted-ao/develop/install.sh | sh'`. Confirm the output ends in exactly one line matching `^ao-pair://v1/`, with no password prompt hang (NOPASSWD sudo is the Azure cloud-init default for `azureuser`, confirm it held).
- [ ] **Step 3:** Copy the printed string, paste it into the desktop app's Add Machine flow (Phase 1's paste-first `AddPairedMachineDialog`). Confirm the machine appears in the picker, the board loads, a terminal opens, and `ao doctor` runs through the gateway.
- [ ] **Step 4:** Re-run the installer a second time on the same box. Confirm it does not print a new full pairing string (idempotent `ao setup-vm --pair`/`ao pair show` behavior per Phase 1) and does not restart the daemon (no dropped agent sessions if any were running).
- [ ] **Step 5:** `ssh azureuser@20.197.63.75 'sudo ao vm rotate-passcode'`, confirm a fresh full `ao-pair://` string prints, re-paste it in the desktop app, confirm the machine reconnects under the new passcode with the same pinned certificate fingerprint (no re-pair prompt).
- [ ] **Step 6:** Record the run (exact commands, timestamps, the box's `systemctl status ao-daemon.service ao-gateway.service` output) in `docs/STATUS.md` or a short note under `docs/plans/`, matching how Phase 0's merge playbook records its own post-merge verification.

## Explicitly out of scope (do not build here)

- macOS and Windows installers. `ao setup-vm --pair`'s own platform gate (`checkSetupPlatform` in `setupvm_plan.go`) refuses anything that is not Debian-family Linux with systemd and apt today; supporting macOS end to end would mean building launchd-based provisioning into the CLI itself, a materially larger change than "the curl|sh installer."
- `get.agentlab.in` DNS and hosting. Explicitly deferred to Harshit; Task 4 only makes the eventual swap a one-line docs change.
- Any change to `ao setup-vm --pair`'s provisioning logic, systemd unit content, certificate, or passcode handling. All of that is Phase 1 (string emission) and already-shipped code (provisioning itself); Phase 2 only fetches and runs the binary.
- A systemd-in-Docker CI smoke test (see Task 3's closing note).
- Any control-plane, account, or telemetry touch in `install.sh`.

## Self-review notes

- Task 1 is the actual hard blocker; without it, Task 2's script has nothing correct to download and the whole phase silently degrades to "works if you build from source." Land Task 1 first and verify it with a real `gh release view` before writing a line of `install.sh`.
- The Step 0 coordination note in Task 2 exists because the spec (`ao pair` provisions) and the Phase 1 plan (only `ao pair show` is a new subcommand; `ao setup-vm --pair` keeps provisioning, just with new output) do not fully agree. Get this from the landed code, not from either document, before hardcoding the command name.
- If a future revision adds macOS, the shape to extend is `asset_name(os, arch)` returning a fourth case and `ao setup-vm --pair`'s platform gate growing a Darwin/launchd branch, both isolated changes; nothing in this plan's `install.sh` design assumes Linux-only beyond that one function and the platform gate it calls into.

## Open questions for Harshit

1. **Is macOS in scope for the installer's first cut, or Linux-only?** Today `ao setup-vm --pair` refuses any non-Linux box at the preflight gate (`checkSetupPlatform`), so macOS support means launchd-based provisioning in the Go CLI, materially more work than the rest of this plan combined. This plan scopes v1 to Linux only (matching the design spec's "linux amd64 and arm64" framing).
2. **`ao pair` vs `ao setup-vm --pair` as the box-side verb.** The onboarding spec's prose says `ao pair` provisions; Phase 1's plan, read literally, only builds `ao pair show` as new. Should Phase 1 make bare `ao pair` a true provisioning alias (cleaner product verb), or does `ao setup-vm --pair` stay the permanent provisioning command? One-line change to `install.sh` either way, worth deciding once.

## Sequencing amendment by the orchestrator (2026-08-20)

Task 1 (publish `ao-linux-*` release assets) is being pulled forward: it lands together with the 0.13.0 version bump on `develop` right after PR #98 merges, so the `v0.13.0` release (Harshit's chosen tag, superseding v0.10.3) already carries the CLI binaries the installer needs. Tasks 2-5 still wait for Phase 1.
