# Hosted AO v1 build log

What was built, in what order, and the decisions that are **not derivable from the diff**.
The git log already records what changed; this file records why, where the reasoning lives
only in a review thread, an issue body, or a conversation.

- **Intent** is `docs/superpowers/specs/2026-07-29-hosted-ao-v1-accounts-and-machines.md`.
- **Findings** are the three preserved reports in [`reviews/`](reviews/).
- **The token contract** is `controlplane/TOKEN_CONTRACT.md`.

Everything merged so far landed on `develop` on 2026-07-30, between 08:46 and 14:23 IST,
across 23 pull requests. Nothing in this file is a claim about a deployment: the fresh-VM
run is task 14 and has not happened.

## 1. Batch structure and which PR closed which task

The spec breaks the work into five batches. Batches run in order; tasks inside a batch are
independent and were built in parallel by separate AO workers.

| Task | Batch | PR | Issue | State |
| --- | --- | --- | --- | --- |
| 1. Control plane skeleton and the token contract reference | 1 | [#5](https://github.com/agentlab-in/hosted-ao/pull/5) | [#1](https://github.com/agentlab-in/hosted-ao/issues/1) | merged |
| 2. ADR 0002, hosted public gateway | 1 | [#4](https://github.com/agentlab-in/hosted-ao/pull/4) | [#2](https://github.com/agentlab-in/hosted-ao/issues/2) | merged |
| 3. `cloneUrl` on `POST /api/v1/projects` | 1 | [#6](https://github.com/agentlab-in/hosted-ao/pull/6) | [#3](https://github.com/agentlab-in/hosted-ao/issues/3) | merged |
| 4. Control plane: Google login | 2 | [#12](https://github.com/agentlab-in/hosted-ao/pull/12) | [#7](https://github.com/agentlab-in/hosted-ao/issues/7) | merged |
| 5. Control plane: keys and tokens | 2 | [#13](https://github.com/agentlab-in/hosted-ao/pull/13) | [#8](https://github.com/agentlab-in/hosted-ao/issues/8) | merged |
| 6. `ao vm serve` | 2 | [#14](https://github.com/agentlab-in/hosted-ao/pull/14) | [#9](https://github.com/agentlab-in/hosted-ao/issues/9) | merged |
| 7. Control plane: device flow and registry | 3 | [#26](https://github.com/agentlab-in/hosted-ao/pull/26) | [#23](https://github.com/agentlab-in/hosted-ao/issues/23) | merged |
| 8. `ao setup-vm`, part one | 3 | [#28](https://github.com/agentlab-in/hosted-ao/pull/28) | [#24](https://github.com/agentlab-in/hosted-ao/issues/24) | merged |
| 9. Desktop: AO login | 3 | [#27](https://github.com/agentlab-in/hosted-ao/pull/27) | [#25](https://github.com/agentlab-in/hosted-ao/issues/25) | merged |
| 10. `ao setup-vm`, part two | 4 | [#32](https://github.com/agentlab-in/hosted-ao/pull/32) | [#29](https://github.com/agentlab-in/hosted-ao/issues/29) | merged |
| 11. `ao vm setup-harness claude` | 4 | [#33](https://github.com/agentlab-in/hosted-ao/pull/33) | [#30](https://github.com/agentlab-in/hosted-ao/issues/30) | merged |
| 12. Desktop: machine list | 4 | [#35](https://github.com/agentlab-in/hosted-ao/pull/35) | [#31](https://github.com/agentlab-in/hosted-ao/issues/31) | merged |
| 13. Desktop: authenticated remote transport | 5 | [#54](https://github.com/agentlab-in/hosted-ao/pull/54) | [#48](https://github.com/agentlab-in/hosted-ao/issues/48) | merged |
| 14. Fresh-VM end-to-end verification and docs | 5 | [#68](https://github.com/agentlab-in/hosted-ao/pull/68) | not filed | done (see `hosted-log.md`) |
| 15. Retire the env-var pairing path | 5 | (this change) | not filed | done; accounts-only remote + account home fleet page |

Two pieces of work were added mid-build and are not spec tasks:

- **[#36](https://github.com/agentlab-in/hosted-ao/pull/36)** ([#34](https://github.com/agentlab-in/hosted-ao/issues/34)), `GET /api/v1/doctor`. Task 12 needed remote machine
  readiness in the desktop, and `ao doctor` only existed as a CLI. Exposing the existing
  checks over HTTP was cheaper than a second readiness surface, and it is what made the
  desktop's harness state derivable at all. It is also what turned a hung local probe into a
  hung HTTP request, which the second review round found as H3.
- **[#47](https://github.com/agentlab-in/hosted-ao/issues/47)**, `POST /api/v1/machines/{id}/token`. Found while scoping task 13; see
  section 2.2.

The remaining ten PRs were fixes: [#11](https://github.com/agentlab-in/hosted-ao/pull/11)
(CI, before any feature work, see section 2.4) and #18, #19, #20, #22, #42, #43, #44, #45,
#46 against review findings. Section 3 covers what those rounds found.

## 2. Decisions not derivable from the diff

### 2.1 `publicUrl` is a full origin, and the gateway normalizes it

`machine.json` carries `"publicUrl": "https://vm.example.com"`, a full origin with a scheme.
`ao vm serve` reduces it to a bare hostname with `url.Parse` plus `u.Hostname()` in
`normalizeDomain` (`backend/internal/vmgateway/config.go`) before handing it to
`autocert.HostWhitelist`.

The alternative was the writer emitting a bare hostname: rename the field to `domain` and
have `ao setup-vm` write `vm.example.com`. That was rejected. Three consumers want an
origin and only one wants a hostname:

- the spec describes `setup-vm` as writing a "public URL",
- the `machines.hostname` column is documented as the machine's public URL and is what the
  poll response and `GET /api/v1/machines` return,
- the desktop needs an origin to build an API base URL, and would otherwise have to
  reconstruct one by guessing the scheme.

Only `autocert.HostWhitelist` wants a hostname, so the reduction belongs at that one
consumer. The reviewer laid out both options and recommended this one
([batches 1 and 2](reviews/2026-07-30-review-batches-1-2.md), section 4); the decision was
recorded in issue [#15](https://github.com/agentlab-in/hosted-ao/issues/15) before the fix
was written.

What made this urgent rather than cosmetic is the failure mode, finding H2:
`HostWhitelist` runs `idna.Lookup.ToASCII` and, per its own documentation, **silently
ignores** anything that fails to parse. A full URL therefore left the whitelist empty, so
no certificate was ever issued and every TLS handshake failed while the gateway logged a
clean start. The normalization now rejects loudly at boot rather than at certificate time,
which is the second half of the decision: a value that cannot be reduced to a hostname is a
startup error, not a warning.

The residual asymmetry is recorded and **not fixed**: the control plane keeps a port in
`publicUrl` (it stores `u.Host`) while the gateway drops it (`u.Hostname()`), so
`https://vm.example.com:8443` registers cleanly and then certificates the wrong name. That
is finding L-B in the server-side review, and no merged PR addresses it.

### 2.2 The two-audience token split

There are exactly two access token audiences. They differ only in `aud` and are not
interchangeable in either direction:

| `aud` | Means | Verified by |
| --- | --- | --- |
| `machines.id` | call that machine's gateway | `ao vm serve` on that VM |
| `https://ao.agentlab.in` | call the control plane's API | the control plane itself |

**Why it appeared mid-build.** The original contract had one audience, the machine. Task 7
then added `GET /api/v1/machines`, the authenticated list the desktop consumes, and there
was nothing to authenticate it with: a desktop install that has signed in but has not yet
picked a machine holds no machine id, so it cannot hold a machine-audience token, so it
cannot call the one endpoint that would tell it which machines exist. The refresh token was
the only credential it had, and sending a 90 day credential on an ordinary resource call
was not acceptable. The split resolves that: the refresh token buys a control-plane-audience
token at `POST /api/v1/token`, and that token calls the control plane API.

The control-plane audience is the control plane's own origin, the same string as `iss`,
rather than a separate identifier. There is exactly one control plane and it already
publishes that origin everywhere, so pinning `aud` to it also means a token minted by a
staging control plane cannot be replayed against production even if the two shared a key.

**Why `POST /api/v1/machines/{id}/token` is a separate endpoint rather than an `audience`
parameter on the refresh grant.** Overloading `POST /api/v1/token` would rotate the refresh
token on every machine-token request. Rotation is single-use by design: exchanging a refresh
token revokes the presented one in the same transaction that issues its replacement. So a
desktop that asks for a machine token per machine, or retries one, would be rotating its
long-lived credential each time, and every rotation is another chance to hit the lockout
race that #45 had to fix (a concurrent exchange returning 500, which a client retries with a
token the winner already revoked, forcing a spurious sign-out). Keeping the refresh token on
one path with one job removes the class of problem instead of hardening a wider version of
it. The machine-token endpoint authenticates with a control-plane-audience bearer instead,
through the middleware that already exists.

This endpoint is task 13's prerequisite and is in flight as
[#47](https://github.com/agentlab-in/hosted-ao/issues/47). Until it merges, the desktop
mints no machine-audience token at all, which is why #44 had to make a picked machine report
a distinct not-ready state rather than `ready`: the picker was claiming a connection the app
had no credential to make.

### 2.3 The fork detachment

`agentlab-in/hosted-ao` began as a fork of `Untrivial-ai/agent-orchestrator` and was
detached into a standalone repository. The shared history was **three commits**, ending at
`a1fb47000 chore: scaffold backend/ and frontend/ skeletons for rewrite`, which is the merge
base. Everything either side has done since is independent work on a rewrite scaffold.

Detaching removed three concrete hazards, none of which are visible in any diff:

- **Sync fork.** GitHub's fork UI offers to sync the default branch from upstream. On a
  repository whose entire content diverged at a scaffold commit, accepting that is a
  destructive no-op at best.
- **Wrong PR base.** A fork's new-PR page defaults to the upstream repository. Every PR in
  this build would have been proposed against `Untrivial-ai/agent-orchestrator` unless the
  base was corrected by hand, every time.
- **Actions behaviour on forks.** Fork repositories get different workflow defaults and
  secret availability, and the inherited workflow set includes jobs that are explicitly
  fork-aware. With required status checks on the default branch (section 2.4), a workflow
  that behaves differently on a fork is a merge that hangs rather than a test that fails.

The repository has no parent today, and `git merge-base origin/develop upstream/main`
resolves to the scaffold commit, so both halves of this are checkable from a clone.

### 2.4 The required-status-checks deadlock

Fixed in [#11](https://github.com/agentlab-in/hosted-ao/pull/11), before any feature work.

The ruleset on `develop` requires five contexts: `build-test`, `api-drift`, `lint`, `scan`,
`controlplane-build-test`. Two of the workflows carried `pull_request: paths:` filters, so a
PR touching neither `backend/` nor `controlplane/` never dispatched them.

The distinction that makes this a deadlock rather than a nuisance: **a skipped job reports a
status, a workflow that never runs does not.** A job skipped by a job-level `if:` is
reported to the checks API as success and satisfies its required context. A workflow filtered
out at the `on:` level never starts, so its context has no status at all and sits pending
forever. The PR is not failing, so there is nothing to fix; it is not passing, so it cannot
merge; and the branch is not stale, so re-running nothing changes nothing. A docs-only PR was
permanently unmergeable.

#11 removed the `paths:` filters from `go.yml` and `controlplane-go.yml` so both always
dispatch, and renamed the control plane's `build-test` job to `controlplane-build-test`
because it collided with the Go workflow's context of the same name, meaning the two
workflows were reporting to a single required context and whichever finished last won.

The rule this build follows since: **if a context is required, its workflow must dispatch on
every pull request.** Filter inside the job, never at `on:`.

### 2.5 `AO_REMOTE_URL` and `AO_REMOTE_TOKEN` are retired last, deliberately

The pre-existing remote path is a single shared pairing secret: the desktop sends
`AO_REMOTE_TOKEN` as the `ao_hosted_pair` cookie and Caddy compares it. Accounts and
per-machine tokens replace it entirely. It would have been tidier to delete it when the
gateway landed in task 6.

It is instead task 15, gated on task 14 passing, and every task in between was required to
keep it working (task 13's brief says so explicitly). The reason is sequencing, not
sentiment: the accounts path cannot be exercised end to end until a real VM runs the full
device flow with real DNS and a real certificate, which is task 14, which needs hardware.
Removing the env-var path before that point would leave a window in which the old remote
mode is gone and the new one is unproven, so remote mode would be untestable by any means at
exactly the moment the build most needs to test it. Keeping both means the fresh-VM run has
a working control to compare against.

What is left after task 15: `AO_CONTROL_URL`, which selects which control plane to trust and
can never skip authentication, stays as the development hatch.

### 2.6 Two smaller decisions worth recording

**`machine.json` stays at `~/.ao/machine.json`, not under `AO_DATA_DIR`.** Reported twice
(L6, then L-H) as an inconsistency with `CertDir`, which does honour `AO_DATA_DIR`. #43
closed it as a documented decision rather than a relocation: `ao setup-vm` writes
`<home>/.ao/machine.json` regardless of `AO_DATA_DIR`, so deriving the gateway's read path
from the data dir would have moved the file the gateway reads without moving the file
setup-vm writes, leaving an operator who set `AO_DATA_DIR` with a gateway looking where
nothing ever writes. `machine.json` is binding identity and has the same shape as
`running.json`: pinned to `~/.ao`, moved only by its own override (`AO_MACHINE_FILE`, which
is what the systemd unit sets). The real defect was that the path was spelled out in three
places; `DefaultMachineFilePath` is now exported, carries the reasoning, and is pinned by a
test.

**`/api/v1/doctor` was deliberately not added to `lanControlBlockedPrefixes`.** That list is
the loopback-only *control* surface: things that change state or that only the daemon's
operator may reach. Doctor is a read-only readiness report, the LAN listener already requires
the Connect Mobile password, and a mobile readiness view is a plausible consumer. Recorded in
#43 so it reads as a decision and not an oversight, along with what a LAN client holding that
password can still read: tool versions, the daemon's loopback port, the database path and
size, and a hooks log line.

## 3. The review rounds

Two rounds, three reports, all preserved in [`reviews/`](reviews/) because `/tmp` is
ephemeral and they are the only record of which findings were **confirmed** versus
**suspected**, and of what was read and found correct and therefore does not need
re-reviewing.

| Report | Scope | Reviewed at | Findings |
| --- | --- | --- | --- |
| [batches 1 and 2](reviews/2026-07-30-review-batches-1-2.md) | everything merged through #14 | `193f6cc52` | 17: 0C 4H 5M 8L |
| [batches 3 and 4, server](reviews/2026-07-30-review-batches-3-4-server.md) | control plane, gateway, tokens, daemon HTTP | `a7eb9c6b2` | 15: 0C 2H 5M 8L |
| [batches 3 and 4, client](reviews/2026-07-30-review-batches-3-4-client.md) | CLI, `ao setup-vm`, desktop | `a7eb9c6b2` | 22: 1C 4H 9M 8L |

Both rounds were read-only: the reviewers filed no PRs and no issues, and the orchestrator
decided what got fixed. The findings were then grouped into fix issues by owning package, so
each fix PR touched one area and the parallel workers could not collide: #15 the gateway,
#16 the control plane, #17 the clone path, #21 the cross-module test, #37 the device flow,
#38 setup-vm, #39 doctor and gateway, #40 the desktop, #41 the origin-URL writers.

All 54 findings are mapped to the PR that fixed them in each report's header, with one
exception, L-B, which no merged PR addresses.

### 3.1 The two findings whose tests were asserting the bug

Both rounds turned up a test that was green **because of** the defect, which is the reason a
passing suite was not treated as evidence.

- **C1**, the critical one. `setupvm_plan.go` emitted `WorkingDirectory=%q`, producing
  `WorkingDirectory="/home/ubuntu/.ao/data"`. Unit-file quoting is only honoured by settings
  parsed as a list of words; `Environment=` is one, `WorkingDirectory=` is not, so systemd
  sees a value starting with a double quote, fails `PATH_CHECK_ABSOLUTE|PATH_CHECK_FATAL`,
  and **refuses to load the unit** after apt has run, the binary is installed, and both units
  are written. The assertion at `setupvm_plan_test.go:593` was
  `strings.HasPrefix(value, "\"/")`. That assertion existed to prove the path was absolute,
  and it was passing precisely because of the leading quote that made it not absolute. Two
  more golden assertions at `:525` and `:554` baked in the quoted form. The suite was green,
  the units were unloadable, and the test that was supposed to catch it had been written
  against the broken output.
- **L4** in the first round. `TestGateway_Mux_UsesCookieNotHeader` deliberately asserted that
  `/mux` accepts the cookie and never the `Authorization` header. That is the narrowing the
  finding objects to: it locks out the CLI, a test harness, and any non-browser client. The
  test was not wrong about the code; it was pinning behaviour nobody had decided to want.

### 3.2 Known gaps

- **L-B is open.** The control plane keeps a port in `publicUrl`, the gateway drops it, and
  nothing checks the two agree. No issue was filed.
- **Five pre-existing `internal/cli` failures** (`spawn_test.go`, "daemon returned HTTP 404")
  are unrelated to this build and were confirmed unrelated by both review rounds: `ao spawn`
  calls `GET /api/v1/agents` against a stub that only serves `/api/v1/projects`. They
  reproduce on `develop` with an isolated `AO_DATA_DIR` and `AO_RUN_FILE`, and none of the
  hosted AO commits touch `spawn.go` or the agents route.
- **No end-to-end evidence exists yet.** Everything above was verified by reading code and
  running test suites. The gateway has never obtained a real certificate, the device flow has
  never run against real DNS, and `ao setup-vm` has never been run on a VM. That is task 14,
  and section 3 of the client-side review is the ranked pre-flight checklist for it.
