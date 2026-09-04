# Backend intake handoff v2

Supersedes the earlier artifact set. Prepared for owner hosted-ao-90, conductor PR #150. No PR operation was performed by this support session.

Frozen downstream: `78df8602e8aa5a482da50d46ec8cf6d175d36535`.
Frozen upstream: `2e0614fc2b64c44f5e62b5983f0ebbc03ff5a3e5`.
Base: `ab6c1f2d2c3695f7023f85692b70d388fe63a018`.

## Apply the resolved-file archive

`backend-resolved-sources.tar.gz` contains exactly the nine backend source/test files listed in `manifest.json`. It replaces those files in the owner's existing true merge, including complete source conflict resolutions and nonconflicting upstream changes. It contains no migrations, generated files, tracked evidence docs, AGENTS.md, workflow, forge config, or gateway route contract. No owner workspace was touched.

Inspect and extract from the owner's merge workspace:

```sh
support=/Users/harshitsinghbhandari/.ao/data/worktrees/hosted-ao/hosted-ao-91/.intake-support/artifacts-v2
tar -tzf "$support/backend-resolved-sources.tar.gz"
tar -xzf "$support/backend-resolved-sources.tar.gz"
```

Then inspect and stage only the intended source resolutions. Generated output remains the owner's regeneration responsibility. The archive avoids dependence on merge conflict labels or conflict-marker style.

Plain patch alternatives are included. `01-conflict-sources.patch` expects the five source conflict files restored to downstream, as enumerated in the first five manifest entries. `02-state-root-regressions.patch` expects normal clean auto-merge output. Use either the archive or the patches, not both. Do not apply the superseded `02-hao-boundaries.patch` from artifacts v1.

## Owner policy update and conflicts

- Keep all HAO state-root restrictions. Correct the upstream config test to use StateRootSegments; actual StateDir behavior already resolves under the hosted root.
- Retain upstream LAN's exact GET `/api/v1/identity` exemption. It remains method/path-specific; ordinary LAN routes retain bearer authentication and lockout. This supersedes the v1 support recommendation.
- Hosted gateway auth, gateway denylist, pair-mode auth, and the gateway route contract remain byte-identical to frozen downstream. No gateway changes are in this archive or either patch.
- Policy conflict: upstream newly marks `/api/v1/agents/codex` and POST agent-install routes as LAN-blocked controls, while the unchanged hosted gateway routes them to the loopback daemon. The daemon's account-origin boundary accepts native clients without Origin, so it does not substitute for gateway path denial. The owner must explicitly decide whether to extend the gateway denylist or accept this new remotely reachable control surface. This handoff follows the owner's instruction to leave it unchanged.
- Policy conflict: upstream wires cloudflared public tunnels into Connect Mobile. Current HAO Mobile scope is home-network-only, and the owner has not authorized this change. The archived daemon omits production ResolveTunnel/ManagedTunnel wiring. The implementations remain inert. Frontend/mobile intake must align with that behavior, or the owner must explicitly authorize a new remote-access policy.

## Source conflict decisions

| File and hunk | Base intent | Upstream intent | Downstream intent | Resolution |
| :--- | :--- | :--- | :--- | :--- |
| `cli/root.go`, Deps | Injectable thin CLI dependencies | Add `RunInteractiveCommand` with injectable streams and `ReadSecret` for Codex login | Add foreground `RunInteractive` for VM harness login | Keep both APIs. They serve different call sites and terminal ownership contracts. |
| `cli/root.go`, DefaultDeps | Populate ordinary CLI dependencies | Wire the two new Codex dependencies | Wire VM interactive login and shared doctor package constants | Keep all three new dependencies; use `doctor.DefaultGitHubRESTBase` and `doctor.DefaultGitLabRESTBase`. Do not restore obsolete CLI-local constant names. |
| `cli/root.go`, withDefaults | Fill missing dependencies for tests/callers | Default Codex command runner and secret reader | Default VM foreground runner | Keep all three independent nil guards. |
| `cli/root.go`, telemetry exclusion | Exclude internal/bootstrap processes | Exclude `chat-host` and `codex-login` | Exclude `vm serve` so gateway startup never depends on daemon telemetry | Union exclusions. Keep the gateway comment and all existing exclusions. |
| `daemon/daemon.go`, project service construction | Wire store, sessions, harness and telemetry | Add structured project logging | Place clone-by-URL repos under `cfg.DataDir/repos` | Pass both `Logger: log` and `ReposRoot: filepath.Join(cfg.DataDir, "repos")`. Keep HAO scratch seeding, doctor, tracker and terminal wiring. |
| `apispec/specgen/build.go`, operation helpers | Existing service operation registry | Add identity and endpoint-discovery operations | Add machine doctor operation | Keep complete doctor, identity, and endpoint helpers and all registrations. Retain upstream exact GET identity exemption on LAN, per owner update. Preserve source DTO registrations and regenerate output. |
| `service/project/service.go`, Service fields | Existing project service state | Add logger for degraded/missing-folder reporting | Add reposRoot for managed clone URL support | Keep both fields and the downstream `addMu` critical section. |
| `service/project/service.go`, Deps fields | Injectable project collaborators | Optional Logger | Optional ReposRoot | Keep both optional dependencies. |
| `service/project/service.go`, constructor fields | Existing defaults | Wire logger and fall back to slog.Default | Wire reposRoot | Keep both assignments and upstream logger fallback. |
| `storage/sqlite/db.go`, startup guard/preparation | Repair known lineage histories, then goose.Up with allow-missing | Prepare pre-existing reviewer config schema plus new known-history repairs | Reject foreign lineage before goose writes | Retain both. Move read-only foreign-history rejection ahead of every repair write. Correct reviewer preparation to canonical 121, never 118. |

The migration correction is necessary even though its function body applies textually cleanly. Upstream's helper claims 118 while migration `0121_session_reviewer_agent_config.sql` owns the column. A database with that column already present and history through 117 otherwise skips the cancelled-turn migration and fails on duplicate reviewer column at 121. The new regression fails with the upstream ordinal and passes with the correction, including a second startup.

## Migrations and generated contracts

Every shipped downstream migration remains byte-for-byte unchanged in the fixture and patches. In particular, retain downstream 0118's explicit transaction and UNIQUE retry-source index. The upstream file lacks those downstream fixes; choosing the upstream migration directory wholesale would regress them. The only incoming migration files are 0119 through 0125, at their existing ordinals. No duplicate ordinal exists. See `migration-check.txt`.

Preserve upstream repairs for installer, inventory, Codex lineage, cancelled staging tables, and schema reconciliation. Do not remap canonical migrations or delete durable data merely to silence startup. Reviewer preparation now records 121. Foreign-history rejection occurs before any of these repairs mutate history. A regression seeds foreign 27 plus a repair-eligible installer table and asserts that rejection leaves the ledger unchanged.

All five OpenAPI conflicts are generated layout collisions, not independent source choices:

1. Codex operations collide with HAO doctor insertion. Keep both source registrations.
2. Endpoint/event operation output collides with HAO project-add cloneUrl description and responses. Keep each source operation's own responses.
3. Generated mobile/settings output collides with HAO terminal-theme operation. Keep the terminal-theme source registration and DTO.
4. CloneProjectInput property layout collides with generated input placement. Preserve service input definitions, not either arbitrary YAML hunk.
5. Clone input required/remoteUrl placement collides with downstream schema placement. Generate required properties from the concrete input type.

The distinct project APIs remain distinct: POST projects accepts HAO AddInput.cloneUrl with managed reposRoot; POST projects/clone uses CloneInput.remoteUrl and destinationParent. Do not rename either wire field to make YAML align.

Owner regeneration sequence, after source conflicts are resolved:

```sh
npm run sqlc
npm run api
cd backend
go test ./internal/storage/sqlite/... ./internal/httpd/... ./internal/cli ./internal/config ./internal/service/project ./internal/daemon ./internal/vmgateway ./internal/domain
```

Stage generated sqlc output plus OpenAPI and frontend schema together. No generated output is included in these patches. For local verification only, the fixture used the untouched frozen upstream OpenAPI after an initial conflicted copy demonstrated parse failures. This permits controller/auth tests but does not establish merged-spec parity. The merged spec drift check remains the owner's required verification.

## Focused verification and limits

The v2 policy rerun covers daemon, httpd (including upstream identity exemption tests), specgen excluding TestBuild_MatchesEmbedded, and unchanged gateway tests. See verification-policy.log.

The unchanged source resolutions previously passed full SQLite/store/project/domain/config/CLI suites and controller tests. Reviewer repair was tested both ways: original 118 ordinal fails with duplicate reviewer column; corrected 121 passes and survives a second startup. Foreign-history refusal leaves a repair-eligible foreign ledger unchanged. Evidence is retained in this artifact directory.

All source verification used an archive-based fixture, not a committed or in-progress merge. The fixture's embedded OpenAPI was copied untouched from frozen upstream solely to permit controller tests; no generated contract was manually resolved or regenerated here. Owner must regenerate sqlc, OpenAPI, and TypeScript in the real merge and run full drift/route parity checks. These remain explicitly unverified.

The archive member list and contents were verified against the nine tested fixture files. The plain patches were independently applied to their documented baselines and checked for byte equivalence. No merge, commit, cherry-pick, push, PR operation, or owner-worktree mutation was performed.

Unresolved owner actions: decide the two policy conflicts above, coordinate mobile/frontend accordingly, regenerate all contracts, and run final integrated validation. This work is source resolution support, not the later independent review.
