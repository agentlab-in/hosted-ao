# Merging upstream agent-orchestrator into hosted-ao

How to think before pulling `Untrivial-ai/agent-orchestrator:main` into this
repo. This is not a runbook you execute top to bottom without reading. It is the
set of judgements the merge needs, in the order you need them, plus the list of
things that must survive it.

Read this whole file before starting. The dangerous parts of this merge are
silent: nothing fails, tests stay green, and a boundary this fork exists to
maintain quietly goes back to the way upstream does it.

## 1. What this repo is, relative to upstream

`agentlab-in/hosted-ao` is **not** a GitHub fork. `isFork` is false and there is
no parent. It is an independent repository that shares git history with
`Untrivial-ai/agent-orchestrator`, wired up through a remote:

```
upstream  https://github.com/Untrivial-ai/agent-orchestrator.git
origin    https://github.com/agentlab-in/hosted-ao
```

That distinction matters. GitHub will not compute a comparison for you, there is
no "sync fork" button, and nothing on the platform is tracking how far the two
have drifted. Every merge is a local, manual, deliberate act.

hosted-ao's reason to exist is a **hosting layer** that upstream does not have:
a control plane that owns accounts and machines, a public VM gateway, a desktop
app that can point at a remote machine, and a state root that does not collide
with upstream's. Everything else is upstream's product and hosted-ao should
follow it, not fight it.

That gives you the default posture for the whole merge:

> Take upstream's version everywhere, except where hosted-ao's hosting layer
> deliberately differs. When in doubt, upstream wins. The exceptions are
> enumerated in section 4 and they are the only exceptions.

## 2. Before anything: verify the merge base is real

Do this first. If it fails, stop, because every three-way merge decision below
is computed from the base and a wrong base produces confident garbage.

```bash
git fetch upstream main
BASE=$(git merge-base develop upstream/main)
git log -1 --format='%H %ci %s' "$BASE"

git diff --shortstat "$BASE"..develop
git diff --shortstat "$BASE"..upstream/main
```

**What a healthy base looks like:** each side shows insertions *and* a
proportionate number of deletions, and the two sides report different deletion
counts.

**What was observed on 2026-08-07, which is why this section exists:** the base
resolved to a commit dated 2026-05-26, roughly 1900 commits behind on our side
and 2000 behind on theirs, and both sides reported the *same* 943 deletions with
enormous insertion counts and near-zero deletions per file. That pattern is not
what genuine divergence looks like. It suggests the shared history is weaker
than it appears, for example an imported or squashed history rather than a true
common ancestor.

If you see that pattern, do not proceed with a plain `git merge`. Investigate
first:

```bash
# Do the two sides actually share the tree at the base?
git diff --stat "$BASE" upstream/main -- backend/internal/config/config.go
# Is the base an ancestor of both, genuinely?
git merge-base --is-ancestor "$BASE" develop && echo "ancestor of ours"
git merge-base --is-ancestor "$BASE" upstream/main && echo "ancestor of theirs"
```

If the base is not trustworthy, the merge becomes a **port**, not a merge: you
pick upstream changes subsystem by subsystem rather than asking git to reconcile
two trees. That is slower and it is the honest option. Do not paper over a bad
base by taking `--theirs` in bulk.

## 3. Sizing the job honestly

Get these numbers before you commit to a plan, and write them in the merge PR
description so a reviewer knows what they are looking at:

```bash
BASE=$(git merge-base develop upstream/main)
git diff --name-only "$BASE"..develop        | sort > /tmp/ours.txt
git diff --name-only "$BASE"..upstream/main  | sort > /tmp/theirs.txt

comm -12 /tmp/ours.txt /tmp/theirs.txt | wc -l   # both touched: the conflict surface
comm -23 /tmp/ours.txt /tmp/theirs.txt | wc -l   # only we touched
comm -13 /tmp/ours.txt /tmp/theirs.txt | wc -l   # only they touched
```

As of 2026-08-07 that was **1352 files touched by both sides**. A conflict
surface in the thousands is not a single sitting and it is not one PR. Plan for
a sequence of scoped merges (section 6) rather than one heroic branch that is
never reviewable and never lands.

Also list what is genuinely ours, because those files cannot conflict and should
never appear in a conflict list. If one does, something is wrong with your merge
strategy, not with the file:

```bash
git ls-tree -r --name-only develop      | sort > /tmp/ours-files.txt
git ls-tree -r --name-only upstream/main | sort > /tmp/theirs-files.txt
comm -23 /tmp/ours-files.txt /tmp/theirs-files.txt
```

That was 338 files, concentrated in `controlplane/**`,
`backend/internal/vmgateway/**`, and the machine-aware parts of
`frontend/src/main` and `frontend/src/shared`.

## 4. What must not change

These are the invariants hosted-ao exists to hold. Upstream has no reason to
respect any of them, so a merge will attack them by accident. Each one below is
a thing to grep for *after* the merge resolves, not just during.

### 4.1 The state root: `~/.ao/hosted`

**The single most dangerous regression in this merge.**

Upstream writes `running.json` and `ao.db` directly under `~/.ao`. hosted-ao puts
everything under `~/.ao/hosted`. If a merge reverts that, the two builds fight
over daemon discovery, the pid, the port, and a SQLite file whose goose
migration history the other build owns. Nothing errors. It corrupts quietly, on
a machine that has both builds installed.

Guard:

- `config.StateRootSegments()` (Go) and `frontend/src/shared/state-root.ts`
  (main/renderer) are the only places the path is spelled. Any upstream hunk
  that re-spells a state path is a revert, not an improvement.
- `frontend/src/main.ts` pins Electron `userData` to `~/.ao/hosted/electron`.
  Upstream has no such pin and its version of this file will not have it.
- Nothing may resolve to `~/Library/Application Support` or any OS-default
  app-data location.

After merging, before anything else:

```bash
grep -rn --include='*.go' --include='*.ts' -e '"\.ao"' -e "'\.ao'" \
  backend frontend/src controlplane | grep -v hosted
grep -rn 'userData' frontend/src/main.ts
grep -rn 'Application Support' backend frontend/src controlplane
```

### 4.2 The network boundary

From `AGENTS.md`, and non-negotiable:

- The daemon's loopback listener stays on `127.0.0.1` and stays
  **unauthenticated**. Do not let a merge add auth to it or change its bind.
- The only other permitted network-facing bind is `ao vm serve`, the VM gateway,
  a **separate process** from the daemon (ADR 0002). It is never collapsed into
  the daemon, however tempting an upstream refactor makes it look.
- The opt-in LAN listener (ADR 0001) keeps its bearer-password middleware and
  keeps the loopback-gated control routes (`/shutdown`, telemetry, mobile
  control) unreachable.
- Neither the gateway nor the LAN listener may start proxying the loopback-gated
  control routes.

Upstream does not have `backend/internal/vmgateway/**` at all, so the gateway
itself cannot conflict. The risk is upstream refactoring the *daemon* in a way
that changes what the gateway proxies to, or moving a route between the
gated and ungated sets. Re-read `vmgateway/proxy.go`'s allowlist and
`blockedAPIPrefixes` after any merge that touches daemon routing.

### 4.3 The renderer CSP

`frontend/vite.renderer.config.ts` allows `https:` and `wss:` in `connect-src`
so the packaged renderer can reach a registered machine's gateway. Upstream has
no registered machines, so **upstream's version pins `connect-src` to
`127.0.0.1` and a merge will silently restore that.**

The failure mode is nasty: dev builds keep working, because Vite proxies `/api`
to loopback. Only a packaged build against a real machine fails, with
`Refused to connect ... violates the document's Content Security Policy`. That
means CI will not catch it and you will find it on a demo.

Check after merging:

```bash
grep -n 'connect-src' frontend/vite.renderer.config.ts   # must contain https: and wss:
```

### 4.4 The account and machine model

`controlplane/**` does not exist upstream (zero files), so it cannot conflict.
What *can* conflict is the desktop side that consumes it, because those files
also exist upstream:

- `frontend/src/main/ao-machines.ts`, `machine-transport.ts`,
  `remote-daemon.ts`, `ao-machine-token.ts`, `ao-control-token.ts`
- `frontend/src/shared/ao-machines.ts`, `remote-daemon.ts`, `control-plane.ts`
- `frontend/src/main.ts` and the preload bridge, where the `machines.*` IPC
  channels are registered
- `frontend/src/renderer/lib/api-client.ts`, whose `runtimeFetch` rebases calls
  onto the active machine's base URL and attaches the gateway bearer

Upstream's versions of the shared ones assume a single local daemon. Taking
theirs wholesale removes remote machines. These need line-level judgement.

### 4.5 Migrations

- Never modify an already-merged SQLite migration. Add a new one.
- Upstream will have added migrations since the base. **Numbering will
  collide.** Two migrations with the same ordinal from two lineages is a merge
  conflict git cannot see, because the files have different names and both apply
  cleanly to the directory.
- Before merging, list both sides' migration directories and plan the
  renumbering deliberately. Then run the migration chain from empty on a scratch
  data dir and confirm it applies in order.

The control plane has its own goose history under
`controlplane/internal/storage`, entirely ours, and is not affected.

### 4.6 Generated files: never merge, always regenerate

Merging generated output produces plausible garbage that compiles. These are
generated and must be rebuilt from source after the merge resolves, not
hand-reconciled:

| File | Regenerate with |
|---|---|
| `backend/internal/httpd/apispec/openapi.yaml` | `npm run api:spec` |
| `frontend/src/api/schema.ts` | `npm run api:ts` (or `npm run api` for both) |
| `backend/internal/storage/sqlite/gen/*` | `npm run sqlc` |

`openapi.yaml` was the single highest-churn shared file in the last measurement.
Do not spend a minute resolving it by hand. Take either side, then regenerate
and commit the result.

## 5. What is fine to take

Default to upstream for everything that is upstream's product. Being behind
upstream is a cost hosted-ao pays for nothing, and `DESIGN.md` already says the
renderer clones the agent-orchestrator web app verbatim, so taking upstream UI
is *aligned with*, not contrary to, this repo's design rules.

Take theirs, with normal review and no special ceremony:

- `backend/internal/adapters/**`: agent harnesses (claudecode, codex, copilot,
  kiro, opencode, vibe, and the rest), `runtime/tmux`, `scm/github`,
  `tracker/github`, `workspace/gitworktree`. This is the bulk of the churn and
  almost none of it touches hosting.
- Renderer components, styling, and shadcn primitives that are not machine-aware.
- Test fixtures and test-only helpers for upstream subsystems.
- Dependency bumps, lockfiles, tooling, CI for upstream-owned jobs.
- Docs that describe upstream behaviour.

The heuristic: **if the change would make sense in a world with no control plane
and no VMs, take upstream's version.** If it only makes sense because hosted-ao
hosts things, it is ours and needs judgement.

## 6. How to actually sequence it

Do not open one branch called `merge-upstream` and try to resolve 1352 files.
That PR is unreviewable, unrevertable, and will rot.

Sequence it so each step is independently reviewable, independently verifiable,
and independently revertable:

1. **Land the base check.** Section 2. If the base is bad, stop and convert to a
   port. Write down which it is.
2. **Freeze.** Do not merge unrelated feature PRs into `develop` while the merge
   is in flight. Every one of them widens the conflict surface.
3. **Take the safe bulk first.** Adapters, harnesses, upstream-only subsystems.
   One PR per coherent subsystem. Each one should be nearly conflict-free; if it
   is not, you have mis-classified it, so re-read section 4.
4. **Then the shared-but-mechanical.** Dependency bumps, lockfiles, tooling.
5. **Then regenerate.** `npm run api`, `npm run sqlc`. Commit generated output
   separately from hand-written changes so a reviewer can skip it.
6. **Then the hosting seams, one at a time, slowly.** `frontend/src/main.ts`,
   the machine transport files, `api-client.ts`, `config.go`, the CSP. These are
   the files where you read every hunk. Budget real time here; it is a small
   number of files and most of the risk.
7. **Then migrations.** Renumber deliberately, apply from empty, verify order.

Each step is a PR into `develop`, which is protected and requires the five
status checks. Do not try to bypass that for a merge; the checks are the only
mechanical guard you have.

## 7. Verification that actually proves something

CI green is necessary and not sufficient. The regressions this merge causes are
mostly invisible to the test suite, because the suite runs against a local
daemon and the hosting layer is what breaks.

Mechanical gates:

```bash
npm run lint                  # backend go test ./... + golangci-lint
npm run frontend:typecheck
cd frontend && npm test && npm run typecheck:e2e
```

Then the things CI cannot see. Do all of these before declaring the merge done:

- **State root.** Start the app, confirm `~/.ao/hosted/running.json` appears and
  nothing is written to `~/.ao` directly or to `~/Library/Application Support`.
- **Packaged build, not dev.** `npm run make`. Several hosting bugs only exist in
  a package: the CSP one in 4.3, and the `app://renderer` origin the gateway's
  CORS allowlist expects. A dev build proves nothing about either.
  Build on **Node 20**, matching CI; on newer Node the packager dies mid-extract
  and exits 0 with no output.
- **A real remote machine.** Sign in, switch to a registered machine, confirm the
  board loads, the SSE stream connects, and a terminal attaches. This is the
  single best end-to-end check that the hosting layer survived, because it
  exercises the control plane, the token mint, the cookie, the gateway, CORS,
  CSP, and the proxy in one action.
- **`ao doctor` on a VM.** Confirms daemon, data dir, migrations, and harness
  readiness on the hosted side.
- **The gateway boundary.** Confirm `/shutdown`, telemetry, and mobile control
  are still unreachable through the public gateway, and that a request with no
  token still gets 401 rather than being proxied.

## 8. Rollback

Decide the rollback before you start, not after something breaks.

- Every step in section 6 is its own PR, so rollback is reverting one PR, not
  unpicking a 1352-file merge.
- Tag `develop` before the first merge PR lands:
  `git tag pre-upstream-merge-$(date +%Y%m%d)` and push the tag.
- If a hosting invariant broke and you cannot see which step did it, revert to
  the tag and redo the sequence with smaller steps. Do not debug forward through
  a merge of this size.

## 9. The short version

If you read nothing else:

1. Verify the merge base is real before trusting any three-way merge.
2. Default to upstream. The exceptions are only the hosting layer.
3. `~/.ao/hosted` is the thing most likely to silently revert. Grep for it after.
4. The daemon stays loopback and unauthenticated. The gateway stays a separate
   process.
5. Never merge generated files. Regenerate them.
6. Migration numbering collides across two lineages and git will not tell you.
7. Sequence it into reviewable PRs. One giant merge branch will not land.
8. Verify with a packaged build against a real machine, on Node 20. CI cannot
   see the regressions that matter here.
