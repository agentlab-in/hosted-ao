# Backend intake handoff v3

Supersedes v1 and v2 following the owner's request to preserve documented HAO authentication boundaries and audit released migration history. Resolution support only, not the later independent review.

Frozen downstream: `78df8602e8aa5a482da50d46ec8cf6d175d36535`.
Frozen upstream: `2e0614fc2b64c44f5e62b5983f0ebbc03ff5a3e5`.
Base: `ab6c1f2d2c3695f7023f85692b70d388fe63a018`.

No refetch, rebase, merge, commit, push, PR action, generated-file regeneration, migration edit, or owner-worktree edit occurred. GitHub release/tag contents were read through gh without changing local refs.

## Apply

The archive contains exactly 16 backend source/test files listed in manifest.json. Extract it over the named files in the existing true merge, regardless of conflict marker style. A separate handwritten contract patch keeps the existing HAO gateway prefix manifest in sync with the new Codex denial. This JSON is source policy, not generated output. No reserved AGENTS, workflow, forge config, or tracked evidence docs are included.

```sh
support=/Users/harshitsinghbhandari/.ao/data/worktrees/hosted-ao/hosted-ao-91/.intake-support/artifacts-v3
tar -tzf "$support/backend-resolved-sources.tar.gz"
tar -xzf "$support/backend-resolved-sources.tar.gz"
git apply --check "$support/gateway-contract.patch"
git apply "$support/gateway-contract.patch"
```

If v2 has already been applied and the nine v2 files have not been manually changed, `backend-update-from-v2.patch` is an alternative to extracting the archive. It expects those nine v2 files plus clean frozen auto-merge content for newly touched files. Do not both extract and apply the update patch. The gateway contract patch is required with either path; it expects the unchanged frozen downstream gateway manifest.

Both patch applications and the archive were checked for byte equivalence against the tested fixture. Full clean-merge-to-source replacement is intentionally supplied as an archive rather than a patch dependent on conflict labels.

## Precise LAN boundary evidence

At frozen downstream, these are the applicable rules and executable evidence:

- `docs/adr/0001-lan-listener-for-mobile.md:26` makes the second listener gated by auth; lines 36-43 specify password on REST/WebSocket, per-source lockout, and app API only. Lines 44-47 make it home-network-only.
- `backend/internal/httpd/lan_listener.go:35` constructs `lanControlBlock(authMiddleware(...)(handler))` for the entire shared handler. There is no exempt route.
- `backend/internal/httpd/auth_test.go:70`, `TestAuthRejectsMissingAndWrong`, requires 401 for missing/wrong tokens and 200 for the right token.
- `backend/internal/httpd/auth_test.go:88`, `TestAuthLockoutAfterFive`, requires 429 after five failures even with the right token.
- `backend/internal/httpd/auth.go` calls source lockout and token validation for every request. Its preview cookie path is an alternate credential transport, not anonymous access.

There is no pre-existing identity-specific test or literal route contract forbidding identity: the endpoint is new. The preservation decision follows the existing whole-listener authentication rule and implementation, rather than pretending an old identity test exists. The identity payload is only opaque host ID and API version; the finding is a policy change, not secret disclosure by that DTO itself.

Resolution: remove upstream isIdentityProbe bypass, retain the route behind LAN auth, and leave primary loopback unauthenticated. New tests exercise identity without/with a password, real LAN listener rejection, and lockout. Controller/DTO/spec comments are aligned with the actual source behavior. The owner can resolve AGENTS and imported ADRs to this boundary. Upstream mobile's anonymous discovery assumption requires corresponding frontend/mobile handling; do not silently reintroduce the exemption just to satisfy that flow.

## New route sensitivity and denial parity

| New surface | Behavior and sensitivity | Resolution |
| :--- | :--- | :--- |
| GET `/agents/codex/accounts` and `/accounts/events` | Cached account identity, authentication state, capacity, active account and updates. No raw credential DTO, but personal account-management data. | Deny the entire `/api/v1/agents/codex` prefix on gateway, matching upstream LAN policy. |
| Account ensure, login-terminal, reauthentication, verify/cancel login operations, logout and DELETE account | Create/open login processes, retain/remove account credentials and mutate login/account state. | Same prefix denial, all methods. |
| Account reset-credit consume | Mutates provider-account reset credit using an idempotency key. | Same prefix denial. |
| Account-switch start and recovery | Mutate device-global active account and coordinate live-session switching. | Same prefix denial. |
| POST `/agents/{agent}/install` | `SystemInstallController.startAgent` calls StartAgentOperation; request can install or reinstall host binaries with selected server-owned installer method. | Deny POST install on both gateways, matching LAN. Trailing slash denied. |
| GET installer catalog, install-jobs and per-agent install status | Read-only plans and jobs, including executable paths and bounded installer output already available to authenticated LAN users. | Keep authenticated access and existing wire contract. |
| POST `/agents/{agent}/verify` | Fixed allowlisted executable version probe, no installation or credential verification. It updates persisted verification-job state. | Keep authenticated access, matching LAN's explicit distinction from installation. This is not a read-only operation and is not described as one. |

Source references: `httpd/controllers/codex_accounts.go` Register/RegisterStreams; `httpd/controllers/systeminstall.go` Register/startAgent/verifyAgent; `service/systeminstall/systeminstall.go` Verify; `service/systeminstall/verifier.go` Verify; `httpd/lan_listener.go` blocked prefixes/request predicate; `httpd/cors.go` codexAccountOriginMiddleware.

The account-origin middleware accepts no-Origin native callers. It cannot protect forwarded gateway requests from a remote native client. Denials therefore occur in the shared gateway middleware before auth/proxy, independent of a spoofed loopback Host. Hosted JWT validation and pair passcode verification remain unchanged.

Regression `TestIntakeControlRoutesNeverReachDaemon` enumerates every newly mounted account-management route and POST install, with and without valid credentials, for real hosted and pair handler stacks. All return 404 with zero fake-daemon calls, including matching CORS preflights. Positive checks keep installer reads, bounded verify, identity and prefix siblings authenticated and proxyable. Identity still requires gateway credentials.

`gateway-route-policy.json` gains the Codex prefix and matching fixtures without changing its v1 schema or version. Its path-only format cannot encode a POST-specific rule; that rule is tested directly at the shared handler boundary. No speculative schema extension is introduced. The existing manifest parity test passes.

## Released migration history

Read-only GitHub release/tag inspection confirms:

- Upstream [v0.12.10](https://github.com/Untrivial-ai/agent-orchestrator/releases/tag/v0.12.10), published 2026-08-31, contains cancelled-turn migration 0118, blob `3f0124acff210d55036b17844e33921cb38e598e`.
- Upstream [v0.12.11-nightly.202609021713](https://github.com/Untrivial-ai/agent-orchestrator/releases/tag/v0.12.11-nightly.202609021713), published 2026-09-02, contains reviewer migration 0121, blob `abfbea3e24d4789bcc69e91d9d986f84b096c3c2`, while its db.go still records 118 for the reviewer column. The relevant released source is preserved in released-history.txt.
- Frozen history introduced cancelled turns at `d9ed3abd3` on 2026-08-30; reviewer config at `822a17a91` on 2026-09-02. HAO fixes at `dd5199ec0` and `b4893a928` preserve an explicit 0118 transaction and UNIQUE retry-source index. Every downstream migration is retained byte for byte.
- Hosted published v0.13.1 points at `a3341ebc315fe1ac8befcd66d4975f9fe33292e5` and has migrations only through 0100. Do not conflate Hosted release numbering with upstream migration numbering or claim the reviewer bug shipped in that Hosted release.

The repair never rewrites a migration file. If the reviewer column exists, it records canonical 121 if needed. If that column exists and conversation_turns lacks the cancelled state, it atomically removes only the misleading 118 ledger entry so the existing canonical migration can run. If the cancelled state exists, real 118 history is left untouched. Foreign-history rejection runs before this or any other repair write.

Tests cover: pre-existing reviewer column with no 118; reviewer column plus false recorded 118; correct applied 118 with reviewer column; second startup; and foreign-history nonmutation. The false-118 regression failed under v2 because cancelled remained absent. The earlier original-ordinal regression failed with duplicate reviewer column. Both pass with v3; canonical 118's ledger row ID stays unchanged.

## Source conflict decisions

| File and hunk | Base intent | Upstream intent | Downstream intent | Resolution |
| :--- | :--- | :--- | :--- | :--- |
| `cli/root.go`, Deps | Injectable thin CLI dependencies | Add `RunInteractiveCommand` with injectable streams and `ReadSecret` for Codex login | Add foreground `RunInteractive` for VM harness login | Keep both APIs. They serve different call sites and terminal ownership contracts. |
| `cli/root.go`, DefaultDeps | Populate ordinary CLI dependencies | Wire the two new Codex dependencies | Wire VM interactive login and shared doctor package constants | Keep all three new dependencies; use `doctor.DefaultGitHubRESTBase` and `doctor.DefaultGitLabRESTBase`. Do not restore obsolete CLI-local constant names. |
| `cli/root.go`, withDefaults | Fill missing dependencies for tests/callers | Default Codex command runner and secret reader | Default VM foreground runner | Keep all three independent nil guards. |
| `cli/root.go`, telemetry exclusion | Exclude internal/bootstrap processes | Exclude `chat-host` and `codex-login` | Exclude `vm serve` so gateway startup never depends on daemon telemetry | Union exclusions. Keep the gateway comment and all existing exclusions. |
| `daemon/daemon.go`, project service construction | Wire store, sessions, harness and telemetry | Add structured project logging | Place clone-by-URL repos under `cfg.DataDir/repos` | Pass both `Logger: log` and `ReposRoot: filepath.Join(cfg.DataDir, "repos")`. Keep HAO scratch seeding, doctor, tracker and terminal wiring. |
| `apispec/specgen/build.go`, operation helpers | Existing service operation registry | Add identity and endpoint-discovery operations | Add machine doctor operation | Keep complete doctor, identity, and endpoint helpers and all registrations. Require LAN password for identity, preserving the frozen HAO boundary. Preserve source DTO registrations and regenerate output. |
| `service/project/service.go`, Service fields | Existing project service state | Add logger for degraded/missing-folder reporting | Add reposRoot for managed clone URL support | Keep both fields and the downstream `addMu` critical section. |
| `service/project/service.go`, Deps fields | Injectable project collaborators | Optional Logger | Optional ReposRoot | Keep both optional dependencies. |
| `service/project/service.go`, constructor fields | Existing defaults | Wire logger and fall back to slog.Default | Wire reposRoot | Keep both assignments and upstream logger fallback. |
| `storage/sqlite/db.go`, startup guard/preparation | Repair known lineage histories, then goose.Up with allow-missing | Prepare pre-existing reviewer config schema plus new known-history repairs | Reject foreign lineage before goose writes | Retain both. Move read-only foreign-history rejection ahead of every repair write. Record reviewer config at canonical 121. Repair a false legacy 118 only when its cancelled-turn schema is physically absent; retain real 118 history. |

The migration correction is necessary even though its function body applies textually cleanly. Upstream's helper claims 118 while migration `0121_session_reviewer_agent_config.sql` owns the column. A database with that column already present and history through 117 otherwise skips the cancelled-turn migration and fails on duplicate reviewer column at 121. The new regression fails with the upstream ordinal and passes with the correction, including a second startup.

## Migrations and generated contracts

Every shipped downstream migration remains byte-for-byte unchanged in the fixture and patches. In particular, retain downstream 0118's explicit transaction and UNIQUE retry-source index. The upstream file lacks those downstream fixes; choosing the upstream migration directory wholesale would regress them. The only incoming migration files are 0119 through 0125, at their existing ordinals. No duplicate ordinal exists. See `migration-check.txt`.

Preserve upstream repairs for installer, inventory, Codex lineage, cancelled staging tables, and schema reconciliation. Do not remap canonical migrations or delete durable data merely to silence startup. Reviewer preparation now records 121 and repairs the released helper's false 118 only when its physical cancelled-turn effect is absent. Foreign-history rejection occurs before any of these repairs mutate history. A regression seeds foreign 27 plus a repair-eligible installer table and asserts that rejection leaves the ledger unchanged.

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

## Verification outcome and required owner checks

PASS: race detector on httpd auth/LAN, both gateway boundary stacks, and SQLite intake regressions. See verification-boundaries.log.

PASS: full SQLite/store, httpd, controllers, CLI, config, project service, daemon, gateway, domain, and source specgen tests in the archive-based fixture. The broader command exits nonzero only at generated route/spec parity: the untouched frozen upstream OpenAPI lacks HAO doctor and terminal-theme routes. This is expected until owner regeneration, not a waived merged-build failure. See verification-focused.log for the exact result.

That command skipped TestBuild_MatchesEmbedded and TestDefaultLoadsEmbeddedSpec. The fixture uses unmodified frozen upstream OpenAPI only as a valid test input, never as a proposed merged artifact. Owner must regenerate sqlc/OpenAPI/TypeScript and run the full unskipped contract checks inside the true merge. No claim of generated-contract parity or whole-merge acceptance is made here.

PASS: archive membership, patch applicability, tested-byte equivalence, downstream migration byte preservation, and ordinal uniqueness. See verification-artifacts.txt and migration-check.txt.

Remaining follow-ups: owner regeneration/full drift checks; align AGENTS/imported ADRs and mobile/frontend with retained LAN authentication; align frontend with omitted cloudflared production wiring. The new gateway control-route denials are resolved and covered, not left as a policy question. No release, live public gateway, desktop/mobile acceptance or later independent review was performed.
