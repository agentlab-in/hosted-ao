# Phase 0: Upstream Merge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge upstream/main (230 commits, base 53197448f, verified healthy 2026-08-19) into develop with every hosting invariant intact, upstream's AO Cloud pinned hidden, and the tree ready for the onboarding work in the spec.

**Architecture:** One merge branch off develop carrying a single true merge commit (conflict resolution per the category policy below) followed by small reviewable fix-up commits (regeneration, reapplied hunks, the AO Cloud pin, verification pins). One PR, reviewed commit by commit. The multi-PR sequencing in the playbook was written for a 1352-file surface; this one is 87 files, roughly half mechanical, so one PR is proportionate.

**Tech Stack:** git, Go 1.x (`go test -race`), npm scripts (`npm run api`, `npm run sqlc`, `npm run lint`), Vitest, electron-forge on Node 20/22.

**Spec:** `docs/superpowers/specs/2026-08-19-seamless-machine-onboarding-design.md` (Phase 0 section). Source analyses: `docs/upstream-merge-playbook.md` (judgment framework) and the 2026-08-19 merge-analysis report reproduced in the task details below.

## Global Constraints

- The daemon stays loopback-only (`127.0.0.1`) and unauthenticated; no merge hunk may change its bind or add auth to it.
- All state resolves under `~/.ao/hosted` via `config.StateRootSegments()` (Go) / `frontend/src/shared/state-root.ts` (TS); any upstream hunk re-spelling a state path is a revert, not an improvement.
- `frontend/src/main.ts` keeps the Electron `userData` pin to `~/.ao/hosted/electron`.
- `frontend/vite.renderer.config.ts` keeps `https:` and `wss:` in `connect-src` (upstream's version drops them; packaged builds break silently).
- Generated files are never hand-merged: take either side, then regenerate (`npm run api`, `npm run sqlc`) and commit the output.
- Never modify an already-merged SQLite migration; any new hosted-only migration numbers >= 0100.
- `develop` is frozen for unrelated feature PRs until this lands.
- No em/en dashes in anything written (commit messages, comments, docs).
- Commit messages are conventional (`chore:`, `fix:`, `docs:`) with no AI attribution footer.

---

### Task 1: Preflight and safety net

**Files:**
- No source changes. Git state only.

**Interfaces:**
- Produces: tag `pre-upstream-merge-20260819` on develop; branch `merge/upstream-2026-08-19`; sizing numbers pasted into a scratch PR description at `/tmp/merge-pr-body.md`.

- [ ] **Step 1: Verify the base is still healthy** (it can drift if develop moved)

```bash
cd /Users/harshitsinghbhandari/Downloads/main-quests/hosted-ao
git fetch upstream main && git fetch origin develop
git checkout develop && git pull --ff-only origin develop
BASE=$(git merge-base develop upstream/main)
git log -1 --format='%H %ci %s' "$BASE"          # expect 53197448f 2026-08-07
git merge-base --is-ancestor "$BASE" develop && echo ancestor-of-ours
git merge-base --is-ancestor "$BASE" upstream/main && echo ancestor-of-theirs
git diff --shortstat "$BASE"..develop            # expect ~238 files, +43836/-13956
git diff --shortstat "$BASE"..upstream/main      # expect ~1142 files, +163854/-36942
```

Expected: both ancestor lines print; shortstats show differing, proportionate deletions. If the base moved or the pathological same-deletions pattern appears, STOP and re-run the full playbook section 2 before proceeding.

- [ ] **Step 2: Tag the rollback point and push the tag**

```bash
git tag pre-upstream-merge-20260819 develop
git push origin pre-upstream-merge-20260819
```

- [ ] **Step 3: Record sizing for the PR body**

```bash
git diff --name-only "$BASE"..develop | sort > /tmp/ours.txt
git diff --name-only "$BASE"..upstream/main | sort > /tmp/theirs.txt
{ echo "base: $(git log -1 --format='%h %ci' $BASE)";
  echo "both-touched: $(comm -12 /tmp/ours.txt /tmp/theirs.txt | wc -l)";
  echo "only-ours: $(comm -23 /tmp/ours.txt /tmp/theirs.txt | wc -l)";
  echo "only-theirs: $(comm -13 /tmp/ours.txt /tmp/theirs.txt | wc -l)"; } > /tmp/merge-pr-body.md
cat /tmp/merge-pr-body.md
```

- [ ] **Step 4: Create the merge branch**

```bash
git checkout -b merge/upstream-2026-08-19 develop
```

---

### Task 2: The merge commit, categories a-c (mechanical resolution)

**Files:**
- Modify: everything git reports as conflicted; this task resolves only the files listed in the three policies below.

**Interfaces:**
- Consumes: branch from Task 1.
- Produces: a partially resolved index; Task 3 resolves the remaining seam files before the single merge commit is made at the end of Task 3.

- [ ] **Step 1: Start the merge**

```bash
git merge upstream/main --no-commit || true
git status --short | grep -E '^(UU|AA|DD|AU|UA|DU|UD)' | sort > /tmp/conflicts.txt
wc -l /tmp/conflicts.txt && cat /tmp/conflicts.txt
```

- [ ] **Step 2: Category (a), regenerate-later: take THEIRS, do not hand-merge**

These five are generated or lockfiles; content is rebuilt in Task 4:

```bash
for f in backend/internal/httpd/apispec/openapi.yaml \
         frontend/src/api/schema.ts \
         frontend/package-lock.json \
         frontend/src/landing/package-lock.json \
         packages/mobile/package-lock.json; do
  git checkout --theirs -- "$f" 2>/dev/null && git add "$f"
done
```

- [ ] **Step 3: Category (b), take-theirs mechanical**

`.gitignore`, the 7 README translations (de/es/fr/ja/ko/pt-BR/zh-CN), `backend/go.mod`, `backend/go.sum`, and the 8 `frontend/src/renderer/i18n/*.json`:

```bash
for f in .gitignore README.de.md README.es.md README.fr.md README.ja.md \
         README.ko.md README.pt-BR.md README.zh-CN.md \
         backend/go.mod backend/go.sum \
         frontend/src/renderer/i18n/de.json frontend/src/renderer/i18n/en.json \
         frontend/src/renderer/i18n/es.json frontend/src/renderer/i18n/fr.json \
         frontend/src/renderer/i18n/ja.json frontend/src/renderer/i18n/ko.json \
         frontend/src/renderer/i18n/pt-BR.json frontend/src/renderer/i18n/zh-CN.json; do
  git checkout --theirs -- "$f" 2>/dev/null && git add "$f"
done
```

Our pairing/clone-UI i18n keys are re-added in Task 5 (they are recoverable from `git show develop:frontend/src/renderer/i18n/en.json`); taking theirs here avoids hand-merging 8 JSON files.

- [ ] **Step 4: Category (c), take theirs then reapply our small hunk**

For each file: take theirs, then inspect our side's diff and reapply the fork hunk by hand if the conflict removed it. The fork hunk per file:

| File | Our hunk to preserve |
|---|---|
| `backend/internal/adapters/runtime/conpty/ptyregistry/registry.go` + `_test.go` | state-root default constant points under `~/.ao/hosted` |
| `backend/internal/cli/hooks.go` | state-root default only |
| `backend/internal/observe/scm/observer.go` + `_test.go` | credential-stripping fix on recorded origin URLs; check upstream did not fix it independently before reapplying |
| `frontend/src/main/auto-updater.ts` + `.test.ts` | one guard clause for empty release repo |
| `frontend/src/renderer/components/SessionInspector.test.tsx`, `Sidebar.test.tsx` | machine-aware test setup lines |
| `docs/README.md`, `docs/STATUS.md`, `docs/architecture.md`, `docs/cli/README.md`, `docs/telemetry.md` | hosted-narrative/state-root paragraphs |

```bash
# per file: view what our side changed since base, then resolve
git diff "$BASE"..develop -- <file>
git checkout --theirs -- <file>
# reapply the fork hunk shown by the diff above, then:
git add <file>
```

- [ ] **Step 5: Confirm only seam files remain unresolved**

```bash
git status --short | grep -E '^(UU|AA)' || echo "nothing left (unexpected before Task 3?)"
```

Expected: only files from the Task 3 cluster list remain.

---

### Task 3: The merge commit, category d (seam files, line-level judgment)

**Files:**
- Modify (resolve): the ~51 seam files, clustered below.

**Interfaces:**
- Consumes: partially resolved index from Task 2.
- Produces: the completed merge commit `chore: merge upstream/main (230 commits, base 5319744)`.

Resolution posture for every file in this task: read every hunk on both sides. Default upstream for product behavior; the fork hunk listed per cluster MUST survive. `git diff "$BASE"..develop -- <file>` shows exactly what ours is.

- [ ] **Step 1: State-root cluster (highest risk)**

Files: `backend/internal/config/config.go`, `config_test.go`, `backend/internal/cli/start.go`, `backend/internal/storage/sqlite/db.go`, `backend/internal/session_manager/manager.go`, `manager_test.go`, `frontend/src/shared/daemon-discovery.ts`, `daemon-discovery.test.ts`, `daemon-launch.ts`, `daemon-launch.test.ts`.

Must survive: `DefaultStateDir`/`StateRootSubdir` exports and every default resolving to `~/.ao/hosted/{running.json,data}`. Take upstream's surrounding refactors; keep our path derivation. After resolving:

```bash
grep -rn --include='*.go' --include='*.ts' -e '"\.ao"' -e "'\.ao'" backend frontend/src | grep -v hosted | grep -v node_modules
```

Expected: no output.

- [ ] **Step 2: Doctor-over-HTTP cluster**

Files: `backend/internal/cli/doctor.go`, `doctor_test.go`, `cli/root.go`, `backend/internal/httpd/api.go`, `httpd/controllers/dto.go`, `httpd/apispec/specgen/build.go`.

Must survive: the `GET /api/v1/doctor` route registration, `DoctorController`, `DoctorReportResponse` DTO, and its `schemaNames` entry in `specgen/build.go`. Upstream does not have any of it; conflicts here are adjacency, so keep both sides' additions.

- [ ] **Step 3: Pairing/transport cluster (largest; budget the most time)**

Files: `frontend/src/main.ts`, `preload.ts`, `renderer/lib/bridge.ts`, `renderer/lib/api-client.ts`, `api-client.test.ts`, `renderer/lib/event-transport.ts`, `event-transport.test.ts`, `frontend/e2e/support/fake-bridge.ts`, `renderer/test/setup.ts`, `GlobalSettingsForm.tsx`, `SessionsBoard.tsx`, `SessionsBoard.test.tsx`, `renderer/stores/ui-store.ts`, `renderer/routes/_shell.tsx`, `renderer/hooks/useConversation.ts`, `useSessionInterfaceTransition.ts`, `useSessionWorkspaceFiles.ts`, `useWorkspaceQuery.ts`.

Must survive: the `machines.*` IPC channel registrations, `runtimeFetch`/SSE rebasing onto the active machine's gateway with the Bearer header, session-level TLS pinning wiring, the `userData` pin in `main.ts`, and the paired-machine dialogs' mount points. Take upstream's WorkOS/AO-Cloud sign-in code as dormant additions (do not delete it here; Task 5 pins it off). Upstream's `AO_APPIMAGE` daemon-identity rewrite (1ee29b281) and Windows console fix (c21e3d846) are product improvements: take them, then re-thread our fork lines through the moved code.

- [ ] **Step 4: CSP**

File: `frontend/vite.renderer.config.ts`. Take upstream's `img-src`/alias changes; keep `https:` and `wss:` in `connect-src` (with the existing `ponytail:` tighten-later note). Verify:

```bash
grep -n 'connect-src' frontend/vite.renderer.config.ts
```

Expected: line contains `https:` and `wss:`.

- [ ] **Step 5: Project-clone cluster (real two-sided conflict)**

Files: `backend/internal/service/project/clone.go`, `clone_test.go`, `dto.go`, `service.go`, `service_test.go`, `workspace_registration.go`.

Both sides grew Git-URL onboarding independently (upstream commit 576b813df). Reconcile toward upstream's structure; must survive: `cloneUrl` on `POST /api/v1/projects` (wire name unchanged, CLI mirrors depend on it) and the credential-stripping fix on recorded origin URLs. If upstream's version already strips credentials, prefer theirs and delete ours; verify with `clone_test.go` cases from both sides kept.

- [ ] **Step 6: Remaining singles**

- `frontend/forge.config.ts`, `frontend/package.json`: keep "Hosted AO" appId/productName/publish feed; take upstream's packaging improvements around them.
- `README.md`: hand-write the reconciliation; ours is the product story, theirs is a 371-line rewrite. Keep our Hosted AO framing, absorb their factual updates (new harness counts, features).
- `backend/internal/telemetrymeta/cli.go`: keep our pair-mode provisioning lines (#95), take their telemetry changes.
- `frontend/src/renderer/styles.css`: take upstream wholesale, reapply only the refined-blue accent override (DESIGN.md mandate).

- [ ] **Step 7: Finish the merge commit**

```bash
git status --short | grep -E '^(UU|AA)' && echo "STOP: unresolved files" || true
cd backend && go build ./... && cd ..
git commit -m "chore: merge upstream/main (230 commits, base 5319744)"
```

Expected: build succeeds before committing; commit is the single merge commit.

---

### Task 4: Regenerate generated artifacts

**Files:**
- Modify: `backend/internal/httpd/apispec/openapi.yaml`, `frontend/src/api/schema.ts`, `backend/internal/storage/sqlite/gen/*`.

**Interfaces:**
- Consumes: merge commit from Task 3.
- Produces: fix-up commit `chore: regenerate api spec, schema.ts, and sqlc output post-merge`.

- [ ] **Step 1: Regenerate**

```bash
npm run sqlc && npm run api
```

- [ ] **Step 2: Verify spec/route parity and migration chain from empty**

```bash
cd backend && go test ./internal/httpd/... ./internal/storage/... && cd ..
```

Expected: PASS. Also confirm no migration ordinal collisions materialized:

```bash
ls backend/internal/storage/sqlite/migrations | sort | uniq -d -w4
```

Expected: no output (no duplicate ordinals).

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "chore: regenerate api spec, schema.ts, and sqlc output post-merge"
```

---

### Task 5: Post-merge pins (AO Cloud hidden, mobile-route block test, i18n reapply)

**Files:**
- Modify: the config gate introduced by upstream commit bf5bcb9a8 (locate with step 1; expected in `frontend/src` cloud settings wiring), `backend/internal/vmgateway/proxy_test.go`, `frontend/src/renderer/i18n/*.json`, `CONTEXT.md`.

**Interfaces:**
- Consumes: merged tree.
- Produces: commits `fix(desktop): pin upstream AO Cloud permanently hidden`, `test(vmgateway): pin mobile-device routes as blocked`, `fix(i18n): restore hosted pairing and clone-UI strings`.

- [ ] **Step 1: Locate and pin the AO Cloud gate**

```bash
git show bf5bcb9a8 --stat
git show bf5bcb9a8 | head -120
```

Upstream hides Cloud sign-in when the broker is unconfigured. Pin it: in our build, hard-code the gate to unconfigured/disabled at its single source (a constant, not a user setting), with this comment above it: `Hosted AO pins upstream AO Cloud off permanently; machines are this fork's remote story. See docs/superpowers/specs/2026-08-19-seamless-machine-onboarding-design.md.` Add a Vitest asserting the Cloud sign-in entry does not render (mirror the test upstream's commit touched, inverted).

- [ ] **Step 2: Record the decision in CONTEXT.md**

Append one paragraph to `CONTEXT.md`: upstream AO Cloud is pinned hidden in Hosted AO builds as of this merge, by decision on 2026-08-19; the pin is one constant and reversible.

- [ ] **Step 3: Pin the mobile-device routes as blocked, with a test**

`/api/v1/mobile` is already in `blockedAPIPrefixes` (`backend/internal/vmgateway/proxy.go:55`), so upstream's new `/api/v1/mobile/devices` roster routes are blocked by construction. Pin it against future narrowing: in `proxy_test.go`, find the existing blocked-route table (the test around the assertion "a blocked route must never reach the daemon") and add a case with path `/api/v1/mobile/devices` expecting the blocked status and zero daemon calls, matching the surrounding table's field names exactly.

```bash
cd backend && go test ./internal/vmgateway/ -run 'Block|block' -v && cd ..
```

Expected: PASS including the new case.

- [ ] **Step 4: Restore our i18n keys**

```bash
git diff "$BASE" develop -- frontend/src/renderer/i18n/en.json
```

Re-add every fork key (pairing dialogs, clone UI) shown by that diff to all 8 locale files, translated values copied from `git show develop:frontend/src/renderer/i18n/<locale>.json`.

- [ ] **Step 5: Commit the three pins separately**

```bash
git add <cloud-gate files> && git commit -m "fix(desktop): pin upstream AO Cloud permanently hidden"
git add backend/internal/vmgateway/proxy_test.go && git commit -m "test(vmgateway): pin mobile-device routes as blocked"
git add frontend/src/renderer/i18n && git commit -m "fix(i18n): restore hosted pairing and clone-UI strings"
```

---

### Task 6: Full verification gate

**Files:**
- No new changes unless a check fails; fixes are additional fix-up commits.

**Interfaces:**
- Consumes: all prior commits.
- Produces: a verified branch ready for PR.

- [ ] **Step 1: Mechanical gates**

```bash
npm run lint
npm run frontend:typecheck
cd frontend && npm test && cd ..
```

Expected: all PASS.

- [ ] **Step 2: Playbook invariant greps (all must be clean)**

```bash
grep -rn --include='*.go' --include='*.ts' -e '"\.ao"' -e "'\.ao'" backend frontend/src | grep -v hosted | grep -v node_modules
grep -rn 'Application Support' backend frontend/src
grep -n 'userData' frontend/src/main.ts        # must show the ~/.ao/hosted/electron pin
grep -n 'connect-src' frontend/vite.renderer.config.ts  # must show https: wss:
```

- [ ] **Step 3: Packaged build (Node 20 or 22, never 26; see memory: newer Node exits 0 with no artifact)**

```bash
cd frontend && node --version && npm run make && ls out/make/ && cd ..
```

Expected: a real `.dmg`/`.zip` artifact exists.

- [ ] **Step 4: Live smoke against a real machine**

Start the packaged app. Confirm: `~/.ao/hosted/running.json` appears and nothing writes to bare `~/.ao` or `~/Library/Application Support`; sign-in and machine switch work against a registered machine (board loads, SSE connects, terminal attaches); the paired-machine path still connects. On the VM: `ao doctor` passes; `curl -k https://<vm>/api/v1/mobile/devices -H 'Authorization: Bearer <token>'` returns the blocked status, and a request with no token returns 401.

- [ ] **Step 5: Commit any fixes, then re-run this task from Step 1 until clean**

---

### Task 7: PR and land

**Files:**
- No source changes.

- [ ] **Step 1: Push and open the PR against develop**

```bash
git push -u origin merge/upstream-2026-08-19
gh pr create --base develop --title "chore: merge upstream/main (230 commits)" --body-file /tmp/merge-pr-body.md
```

Extend the body first: sizing numbers from Task 1, the category counts (5 regenerate, 18 mechanical, 13 small-hunk, 51 seam), the AO Cloud pin decision with a pointer to the spec, and intentional omissions (none expected; list any).

- [ ] **Step 2: Review commit by commit, land on green checks, delete the freeze**

After merge: confirm develop's five status checks passed, announce the freeze lifted, and only then start Phase 1 planning.
