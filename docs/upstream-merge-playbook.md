# Upstream maintenance playbook

Use this playbook for every intake of
`Untrivial-ai/agent-orchestrator:main` into `agentlab-in/hosted-ao:develop`.
It is a reusable operating system for the fork, not a record of one merge.

The dangerous failures are silent: a merge can compile and pass local tests
while reverting the state root, remote-machine transport, packaged-app CSP, or
gateway boundary. Record the evidence for every intake and review the hosted
seams explicitly.

## 1. Repository relationship and policy

This is an independent repository with shared Git history, not a GitHub fork:

```text
origin    https://github.com/agentlab-in/hosted-ao
upstream  https://github.com/Untrivial-ai/agent-orchestrator.git
```

`origin/develop` is the hosted-ao integration and release branch.
`upstream/main` is the upstream tracking branch. Configure `upstream` as
fetch-only so an operator cannot accidentally push to it.

The default resolution policy is:

> Take upstream behavior everywhere except at a documented hosted-ao product
> boundary. Preserve the boundary, not the old implementation around it.

Use a true merge. Do not rebase or squash `develop`: released downstream
history, earlier intake decisions, and the exact consumed upstream SHA must
remain auditable. Developers may rebase unpublished personal branches onto the
updated `develop`; do not rewrite shared branches.

## 2. Cadence and ownership

- Run a scheduled intake weekly.
- Start an out-of-cycle intake for an upstream security, data-loss, or critical
  compatibility fix.
- Sync before starting a large hosted feature and several days before a stable
  release, not during the release.
- Assign one merge conductor, one backend reviewer, one desktop reviewer, and
  one release/acceptance owner. A person may hold more than one role, but every
  role must be named in the PR.
- Freeze unrelated merges to `develop` from final conflict resolution through
  acceptance. If the intake cannot finish within two working days, reduce the
  batch or split follow-up work; do not let a large sync branch rot.

## 3. Preflight and evidence record

### Scope boundary: the intake is only the upstream intake

Do not use an upstream intake to repair unrelated downstream behavior, enforce
new product policy, build missing acceptance infrastructure, or collect evidence
for capabilities that upstream does not contain. The scope is the upstream delta,
its merge conflicts, generated artifacts affected by that delta, and regressions
the delta could introduce at an existing hosted seam. Nothing else.

A pre-existing downstream defect, policy mismatch, missing test, or unverified
hosted-only capability is separate follow-up work. Record it as a non-blocking
follow-up and give it its own branch and PR when appropriate. Do not put its fix
on the sync branch, widen the sync diff for it, or block the intake on completing
it. A hosted-only check becomes part of the intake only when the upstream delta
or a conflict resolution changes code on which that behavior depends.

Apply this rule strictly. The existence of a useful adjacent improvement is not
evidence that it belongs in the intake.

Start clean, fetch both sides, and branch from the current remote integration
branch:

```bash
git status --short
git fetch origin develop
git fetch upstream main
git switch -c sync/upstream-YYYY-MM-DD origin/develop

OURS=$(git rev-parse origin/develop)
THEIRS=$(git rev-parse upstream/main)
BASE=$(git merge-base "$OURS" "$THEIRS")

git merge-base --is-ancestor "$BASE" "$OURS"
git merge-base --is-ancestor "$BASE" "$THEIRS"
git log -1 --format='%H %ci %s' "$BASE"
git rev-list --left-right --count "$THEIRS...$OURS"
git diff --shortstat "$BASE..$OURS"
git diff --shortstat "$BASE..$THEIRS"
```

Stop if the base is not an ancestor of both branches. Also stop if both diffs
show suspiciously identical deletion patterns or otherwise look like imported
or unrelated histories. In that case this is a subsystem-by-subsystem port,
not a safe three-way merge.

Measure the conflict surface:

```bash
git diff --name-only "$BASE..$OURS"   | sort > /tmp/hosted-ours.txt
git diff --name-only "$BASE..$THEIRS" | sort > /tmp/hosted-theirs.txt

comm -12 /tmp/hosted-ours.txt /tmp/hosted-theirs.txt | tee /tmp/hosted-both.txt | wc -l
comm -23 /tmp/hosted-ours.txt /tmp/hosted-theirs.txt | wc -l
comm -13 /tmp/hosted-ours.txt /tmp/hosted-theirs.txt | wc -l
```

Put the following in the PR description before resolving the merge:

- upstream and downstream SHAs;
- merge-base SHA and date;
- left/right commit counts;
- files touched by both sides, ours only, and upstream only;
- high-risk shared files;
- named owners and planned acceptance environment;
- rollback tag.

Tag the exact pre-intake `origin/develop` commit and push the tag before the
merge. Use `pre-upstream-merge-YYYYMMDD`, adding a numeric suffix if needed.

## 4. Perform the merge

```bash
git merge --no-ff upstream/main
```

Do not use repository-wide `--ours` or `--theirs`. Resolve by ownership.

### Upstream-owned by default

- agent and reviewer adapters, harnesses, runtimes, SCM and tracker adapters;
- generic daemon, CLI, session, workspace, and renderer behavior;
- generic renderer components, styling, and test fixtures;
- upstream dependencies, tooling, and documentation.

Accept upstream structure first, then reapply the smallest hosted seam needed.
Do not retain obsolete upstream architecture just because hosted code was
embedded in it.

### Hosted-owned by default

- `backend/internal/vmgateway/**` and pair-mode behavior;
- hosted setup, machine binding, and pairing-string behavior;
- remote-machine identity, credentials, transport, and certificate pinning;
- the isolated hosted state root;
- AgentLab release targeting and unsigned-only distribution policy;
- hosted ADRs, contracts, and acceptance tests.

### Shared seams requiring line-by-line review

- `backend/internal/config/**`;
- daemon and HTTP route wiring;
- CLI root, start, doctor, project clone, setup, pair, and VM commands;
- `backend/internal/httpd/controllers/dto.go` and API spec registration;
- SQLite migrations, schema, queries, and store wiring;
- `frontend/src/main.ts`, preload, and shared bridge types;
- API client and event transport;
- machine-aware board, project creation, and settings components;
- `frontend/vite.renderer.config.ts`;
- Electron Forge configuration and release workflows;
- dependency manifests and lockfiles.

Enable Git's recorded-resolution reuse for future intakes:

```bash
git config rerere.enabled true
```

Always inspect a reused resolution before staging it. `rerere` saves typing; it
does not prove that an old product decision remains correct.

## 5. Hosted invariants

Every intake PR must explicitly attest to these invariants.

### State isolation

All application state remains under `~/.ao/hosted`, overridable only through
the documented environment variables. Go derives it through
`config.StateRootSegments()` and Electron/renderer code through
`frontend/src/shared/state-root.ts`. `frontend/src/main.ts` must continue to pin
Electron `userData` to `~/.ao/hosted/electron`.

Nothing may fall back to bare `~/.ao`, `~/Library/Application Support`, or an
OS-default application-data directory. Otherwise upstream and hosted builds can
silently share discovery files and incompatible SQLite histories.

### Network boundary

- The daemon's primary listener stays unauthenticated on `127.0.0.1`.
- The optional LAN listener remains explicit, authenticated, and unable to
  serve loopback-only control routes.
- `ao vm serve` remains a separate gateway process in hosted and pair modes.
- Every gateway request is authenticated before proxying.
- Shutdown, telemetry, and mobile-control routes are never exposed through a
  gateway.
- Pair mode retains its pinned self-signed certificate, passcode verification,
  lockout, and no-control-plane behavior.

After route changes, review both the daemon route table and the gateway's
blocked prefixes/allowlist. A clean textual merge is not evidence that the
security boundary stayed equivalent.

### Remote-machine desktop behavior

Preserve machine selection, credential attachment, certificate pinning,
remote API rebasing, SSE, terminal transport, and local fallback behavior.
The preload bridge and renderer types must remain aligned with Electron main.

The packaged renderer CSP must allow the HTTPS and WSS origins required by a
registered machine. Dev mode is not an adequate CSP test because its proxy can
hide this regression.

### Product identity

Hosted AO owns the account/machine experience and intentionally disables any
overlapping upstream AO Cloud sign-in surface. An upstream merge must not
restore duplicate sign-in, protocol ownership, state paths, update feeds, or
release repositories.

### Unsigned AgentLab distribution

`agentlab-in/hosted-ao` publishes unsigned builds only. Do not add signing or
notarization secrets, signing setup, signed-artifact claims, or a release gate
that requires signed artifacts. User-facing notes must clearly identify the
builds as unsigned where platform warnings are relevant. This policy does not
weaken checksum, provenance, artifact-completeness, or updater-feed checks.

### Migrations

Never modify an already-merged SQLite migration. Detect ordinal collisions
between the two lineages even when Git reports no conflict, renumber only new
unreleased migrations, and test both an empty database and an upgrade from the
latest hosted release. The control plane has a separate migration history.

## 6. Generated artifacts

Never hand-merge generated output. Resolve source files, choose either side as
a temporary conflict resolution if necessary, and regenerate:

| Generated output                              | Source of truth                   | Command            |
| --------------------------------------------- | --------------------------------- | ------------------ |
| `backend/internal/httpd/apispec/openapi.yaml` | controller DTOs and spec registry | `npm run api:spec` |
| `frontend/src/api/schema.ts`                  | generated OpenAPI document        | `npm run api:ts`   |
| `backend/internal/storage/sqlite/gen/*`       | migrations and SQL queries        | `npm run sqlc`     |

Prefer one follow-up commit for generated output so reviewers can distinguish
judgment from deterministic regeneration. Finish with a clean regeneration:

```bash
npm run api
npm run sqlc
git diff --exit-code
```

## 7. Required verification

Run narrow tests while resolving each subsystem, then the repository gates:

```bash
npm run lint
npm run frontend:typecheck
cd backend && go test -race ./...
cd ../frontend && npm test && npm run typecheck:e2e
```

For areas changed by the upstream delta or a conflict resolution, run or obtain
the applicable CI evidence for:

- route/spec parity and generated-artifact drift;
- fresh and released-database migration paths;
- state-root isolation;
- gateway authentication and blocked routes;
- pairing and remote-machine transport;
- cross-platform unsigned artifact builds and checksums;
- secret scanning.

When the upstream delta or a conflict resolution changes packaged desktop,
remote transport, gateway, state-root, or distribution behavior, perform the
applicable product acceptance with a packaged unsigned build and a real or
production-faithful remote machine:

1. Confirm state appears only below `~/.ao/hosted`.
2. Add or select a machine and load its board.
3. Clone or open a project, start a session, receive SSE updates, and attach a
   terminal.
4. Confirm the packaged renderer can reach the gateway without CSP or CORS
   failures.
5. Confirm unauthenticated gateway requests return `401` and blocked control
   routes are not proxied.
6. Run `ao doctor` on the remote machine.
7. Confirm the release artifacts and docs make no signed/notarized claim.

CI green is necessary but not sufficient for a boundary actually changed by the
intake. Record the applicable acceptance evidence in the PR. Do not make
unrelated hosted-only acceptance or missing downstream infrastructure a merge
gate; track it separately.

## 8. PR and rollback

Keep the true merge and small follow-up fixes on one sync branch when the
conflict surface is reviewable. If it is too large for coherent review, pause
and split upstream-owned subsystem ports or preparatory seam extractions into
separate PRs; do not create multiple partial merge commits with ambiguous
ancestry.

The sync PR targets `develop` and includes:

- the evidence record from section 3;
- upstream release notes or a categorized commit summary;
- every conflict and its ownership decision;
- generated-artifact regeneration results;
- CI and manual acceptance evidence;
- intentional omissions and follow-ups;
- the rollback tag.

If an invariant fails before release, revert the intake merge PR or its focused
follow-up commit. If the failure cannot be localized, return to the rollback tag
and repeat with a smaller conflict surface. Do not debug forward through an
unreviewable merge.

## 9. Emergency hotfix flow

Do not combine an emergency release with a broad upstream intake.

1. Branch from the exact affected hosted release tag.
2. Apply the smallest fix and regression test. If it comes from upstream,
   record the upstream commit SHA.
3. Run affected CI, security, migration, and unsigned packaging gates.
4. Publish through the designated AgentLab release conductor.
5. Merge or cherry-pick the fix into `develop` and every open sync branch.
6. Resume the regular intake only after the hotfix is released and reconciled.

## 10. Completion checklist

- [ ] Upstream, downstream, and merge-base SHAs recorded
- [ ] Merge base verified as an ancestor of both sides
- [ ] Conflict surface and high-risk files recorded
- [ ] Rollback tag pushed
- [ ] True merge used; no shared history rewritten
- [ ] Every conflict assigned to upstream, hosted, or shared-seam ownership
- [ ] State, network, remote transport, CSP, identity, and unsigned-release invariants reviewed
- [ ] Migration ordinals and upgrade paths verified
- [ ] API and sqlc artifacts regenerated from source
- [ ] Required CI green
- [ ] Packaged remote-machine acceptance recorded when the intake changed that boundary
- [ ] Pre-existing downstream issues and hosted-only follow-ups kept outside the intake
- [ ] PR targets `develop` and names owners, omissions, and rollback
