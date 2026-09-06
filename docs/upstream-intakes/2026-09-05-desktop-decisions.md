# Frontend intake resolution support

Owner: hosted-ao-90. Support workspace: hosted-ao-92. This is resolution support, not the independent review.

Frozen inputs:

- Base: `ab6c1f2d2c3695f7023f85692b70d388fe63a018`
- Downstream: `78df8602e8aa5a482da50d46ec8cf6d175d36535`
- Upstream: `2e0614fc2b64c44f5e62b5983f0ebbc03ff5a3e5`

No merge, index mutation, commit, cherry-pick, push, or PR operation was performed. All snapshots, candidate files, and checks are inside this worker's `intake-support` directory. No owner files were edited. Per-file `git merge-file -p` was used to inspect blob combinations, not a repository merge. Formatting-normalized temporary snapshots reduced the 35 raw conflicting files to 19 TypeScript semantic conflicts plus 8 translation files and the generated schema. Original raw conflicts are recorded in `conflicts.json`.

## Revised scope from hosted-ao-44

This revision supersedes the original 43-path patch delivery. The owner reserves
`frontend/forge.config.ts`, `.github/workflows/go.yml`, `AGENTS.md`, evidence
documents and generated outputs. Neither patch includes any of those paths.
`frontend/docs/desktop-release.md` is also excluded to leave documentation
integration with the owner. There are now 41 patch paths (37 downstream and
4 new upstream). Re-read the current manifest and discard previously copied
patches. No owner worktree changes were made.

The Forge and release-document decisions below are advisory only. The retained
`forge.config.test.ts` expresses expected native packaging, Hosted identity and
unsigned policy; the owner must implement its reserved config accordingly.
Verification ran against the isolated candidate config described below and does
not certify the owner's eventual config resolution.

## Apply inside the owner's existing true merge

These are plain patches with explicit file-content baselines. Do not apply patch 01 directly over conflict markers. Do not reset the owner's index, abort its merge, or start another merge.

1. Preserve any owner edits to the paths listed in `restore-downstream-paths.txt` for comparison. For each listed path, write the blob from frozen downstream into the working-tree file using `git show 78df8602e8aa5a482da50d46ec8cf6d175d36535:<path>`. This restores only 37 named frontend file contents, not the index.
2. Apply `01-downstream-resolutions.patch` with ordinary `git apply`, without `--index`. It combines upstream changes with Hosted behavior for those files. It deletes the obsolete `useAgentsQuery.ts` hook.
3. `02-upstream-new-file-corrections.patch` applies to the four newly added upstream files `src/main/cloud-auth-local.ts`, its test, `src/main/telemetry-policy-file.ts`, and its test. The new files should already contain their exact upstream contents from the true merge. If owner edits differ, compare them first or restore those four contents from frozen upstream before applying.
4. Inspect the resulting diff, then mark these files resolved in the owner's merge. Do not stage the support directory. There are no API schema changes in either patch.
5. After resolving backend DTO/spec sources, regenerate API artifacts with `npm run api` and run the full frontend checks in the real merged tree.

`patch-manifest.json` records baseline commit and before/after SHA-256 for all 41 paths. `patch-check.log` verifies both patches apply in a throwaway folder and reproduce every candidate hash, including the deletion. `candidate/frontend` is the inspectable result. Its API schema is an unchanged upstream snapshot used only to make focused testing possible, not a proposed resolution. Its extra packages/backend fixtures are read-only frozen inputs needed by tests and are not shipped in the patches.

## Per-conflict decisions

Paths below are relative to `frontend/`. Each row records all three intents and the resolution. Formatting-only hunks retain both branches' non-overlapping semantic edits after normalization. The patches intentionally retain upstream UX changes rather than resolving entire files to downstream.

| Conflict path | Base intent | Downstream intent | Upstream intent and resolution |
| --- | --- | --- | --- |
| `e2e/smoke-t0.spec.ts` | Global home board smoke assertions. | Fixture and formatting maintenance. | New home dashboard and explicit project-board navigation. Use current upstream home assertions and keep merged fixtures. |
| `e2e/support/fake-bridge.ts` | Complete preload test double. | Machine/paired-machine APIs and remote daemon status. | Browser profiles, telemetry policy, shell preference and auth IPC. Union both bridge surfaces. |
| `forge.config.ts` | Upstream executable, callback scheme and resource packaging. | Hosted executable, bundle IDs, AgentLab feed and no `ao-app` registration. | Native SQLite dependency rebuild, include/unpack and completeness checks. Keep native support with Hosted identity and absent callback claims. Owner-reserved, advisory only: unsigned policy correction described below. |
| `src/api/schema.ts` | Generated daemon client contract. | Remote clone request and Hosted API additions. | New readiness, accounts and telemetry contracts. Do not hand resolve or ship. Owner regenerates from merged Go sources. |
| `src/main.ts` | Local daemon/Electron/window lifecycle and cloud auth. | Hosted state root, machine selection, certificate verification, paired transport, native browser/terminal integration and disabled Cloud. | Browser profiles/history, telemetry authority, visibility tracking and Windows shell preference. Union integrations, remove duplicate `session` import, initialize terminal preference alongside certificate pin initialization. Correct new browser fallback to Hosted root. Preserve selected-machine runtime and trusted shell boundaries. |
| `src/main/cloud-auth.ts` | WorkOS auth/store lifecycle. | Cloud pin and Hosted auth behavior. | Local-session revoke helpers and static import cycle. Keep both imports and all Hosted gates. New IPC path gets a separate gate correction. |
| `src/renderer/components/CloneRepositoryDialog.tsx` | Local clone with native destination picker. | Remote clone derives owner-repo name and never asks for a desktop destination. | Avatar and compact picker redesign. Preserve upstream visual structure locally, gate picker to local machines, retain remote validation/name preview and local/remote copy. |
| `src/renderer/components/CreateProjectFlow.tsx` | Folder/clone-to-agent-sheet flow. | Discriminated remote `cloneUrl` vs local path/destination payloads. | Transition timing and checked-out branch propagation. Keep upstream timing, pass `remoteMachine` into clone dialog, retain source discriminant and branch detection. Remove duplicate close setter produced by composition. |
| `src/renderer/components/GlobalSettingsForm.tsx` | Settings sections. | Self-hosting machines and Hosted cloud section. | Harness, Codex accounts and browser profiles. Keep all relevant sections; render CloudCredentials once rather than twice. |
| `src/renderer/components/NewTaskDialog.test.tsx` | API test double. | Partial mock retains machine URL subscriptions. | Readiness-hook mock. Keep both. |
| `src/renderer/components/SessionView.test.tsx` | Session view API mock. | Partial mock retains machine URL access. | Codex accounts/actions mocks. Keep both. |
| `src/renderer/components/SessionsBoard.tsx` | Board grid and orchestrator spawn. | Startup/empty/error handling through shared board body; Cloud unavailable. | New card adapter callback contract and formatted startup errors. Keep downstream board body with new callback names (`onOpenSession`, `onTerminateSession`). Keep Cloud spawn disabled under Hosted policy. |
| `src/renderer/components/SettingsDialog.test.tsx` | Settings navigation tests. | Deep-linked self-hosting page. | Closing settings must not cancel daemon-owned login work. Preserve both tests. |
| `src/renderer/components/SettingsDialog.tsx` | Settings navigation icons and sections. | Hard-drive self-hosting entry and cloud gate. | Browser/harness/accounts entries. Union icon imports and entries, retain cloud gate. |
| `src/renderer/components/Sidebar.test.tsx` | Sidebar query mocks. | Stable local URL and subscription mock. | POST behavior. Preserve partial mock, URL seams and GET/POST. |
| `src/renderer/components/chat/SessionChatSurface.test.tsx` | Chat API mock. | Partial API mock for machine behavior. | Visibility hoisted mocks. Preserve both. |
| `src/renderer/components/chat/TurnSettingsBar.tsx` | Model/settings controls. | AbortSignal passed to model queries. | Readiness-driven controls and current UX. Formatting normalization composes both; retain query cancellation and current upstream controls. |
| `src/renderer/hooks/useAgentsQuery.ts` | Legacy catalog API query. | Request cancellation. | Legacy hook removed in favor of daemon-owned readiness. Accept deletion; no remaining code consumers in candidate. Readiness replaces client-side freshness decisions. |
| `src/renderer/hooks/useShellTerminals.test.tsx` | Shell terminal tests. | Machine-aware mocks and cancellation. | Terminal shell preferences and current fixtures. Compose both after formatting normalization. |
| `src/renderer/i18n/de.json` | German dictionary. | Hosted machine/clone wording and keys. | New UX keys and revised translations. Structured key merge preserves upstream changes plus downstream changes/deletions. Zero conflicting same-key semantic edits. |
| `src/renderer/i18n/en.json` | English dictionary. | Hosted machine/clone wording and keys. | Same structured resolution; zero conflicting same-key semantic edits. |
| `src/renderer/i18n/es.json` | Spanish dictionary. | Hosted machine/clone wording and keys. | Same structured resolution; zero conflicting same-key semantic edits. |
| `src/renderer/i18n/fr.json` | French dictionary. | Hosted machine/clone wording and keys. | Same structured resolution; zero conflicting same-key semantic edits. |
| `src/renderer/i18n/ja.json` | Japanese dictionary. | Hosted machine/clone wording and keys. | Same structured resolution; zero conflicting same-key semantic edits. |
| `src/renderer/i18n/ko.json` | Korean dictionary. | Hosted machine/clone wording and keys. | Same structured resolution; zero conflicting same-key semantic edits. |
| `src/renderer/i18n/pt-BR.json` | Brazilian Portuguese dictionary. | Hosted machine/clone wording and keys. | Same structured resolution; zero conflicting same-key semantic edits. |
| `src/renderer/i18n/zh-CN.json` | Simplified Chinese dictionary. | Hosted machine/clone wording and keys. | Same structured resolution; zero conflicting same-key semantic edits. |
| `src/renderer/lib/api-client.ts` | Loopback HTTP and generic failure reporting. | Per-request remote bearer, one auth retry, bounded timeout, AbortSignal and machine unavailability envelope. | Visibility-owned reporting suppression. Keep remote pipeline and apply suppression to network and token-unavailable errors, preventing duplicate reporting while preserving error responses. |
| `src/renderer/lib/bridge.ts` | Browser-mode bridge defaults. | Paired machine methods. | Profile/history/telemetry/local-auth methods. Compose defaults for both surfaces. |
| `src/renderer/lib/event-transport.test.ts` | Single CDC source fixtures. | Credentialed remote SSE and token-refresh continuity. | Additional Codex account source and shared retry jitter. Select CDC/account sources by URL, retain remote tests, use shared jitter policy. Follow-up 04 replaces the remote-account assertion with local-only stream assertions. |
| `src/renderer/lib/event-transport.ts` | CDC invalidation and reconnect. | Remote credentials, usage invalidation and capped retry. | Account events, visibility ownership and shared jittered backoff. Adopt upstream backoff (60s ceiling before jitter) rather than duplicate downstream 30s state. Preserve remote credentials for CDC. Follow-up 04 keeps account SSE local-only and closes it on remote selection. Keep both invalidation families. |
| `src/renderer/lib/notifications.ts` | Notification stream lifecycle. | Remote credentials and selected-machine transport. | Current reconnect/presentation behavior. Formatting-normalized composition keeps both. |
| `src/renderer/routes/_shell.tsx` | Project creation by local path and shell startup. | Remote clone discriminant, remote-ready status and machine switching/cache handling. | Checked-out default branch, readiness and duplicate-folder UX. Keep typed `CreateProjectInput`; only run local duplicate-path lookup when `input.path` exists. |
| `src/renderer/stores/ui-store.ts` | Sidebar pressure auto-collapse and UI settings state. | Hosted Cloud preference and self-hosting page. | Explicit sidebar visibility, removal of pressure auto-collapse, global toasts and new settings sections. Adopt upstream sidebar policy and global toasts; preserve Hosted additions. |
| `src/shared/telemetry.ts` | Telemetry bootstrap/storage. | Hosted root segments. | Consent-policy bootstrap types. Union imports and keep Hosted storage path. |

## Clean-merge corrections included

These are scoped boundary integrations that text merging alone would miss.

- `src/main/telemetry-policy-file.ts` and its test: upstream introduces a launch-cwd-resolved policy/data directory under bare `.ao`. Neither base nor downstream had this module. Resolve through `STATE_ROOT_SEGMENTS`, retaining explicit `AO_DATA_DIR` and upstream packaged/dev subdirectory behavior.
- `src/main/cloud-auth-local.ts` and its test: upstream introduces dev-local auth IPC independent of the preexisting WorkOS gates. Neither base nor downstream had it. Preserve upstream local-auth helper behavior but require `CLOUD_SIGN_IN_ENABLED` in the live availability gate used by IPC. Assert Hosted loopback dev auth remains unavailable.
- `src/renderer/components/CreateProjectFlow.test.tsx`: upstream-only edits introduce a full API mock without downstream's URL subscription exports. Use a partial mock and stable loopback URL, preserving the new upstream branch/transition tests.
- `src/main/auto-updater.ts` and its test: both sides' cleanly merged update behavior stays, including upstream manual-download discovery and direct feature feeds. Explicitly set macOS full-download policy for both ordinary and feature channels. The frozen versions did not actually contain `disableDifferentialDownload`, despite the task's required policy. This is an explicit requirement correction, not a claimed preexisting implementation.
- Advisory for owner-reserved `forge.config.ts` and release documentation, with `forge.config.test.ts` included: frozen config still enabled signing/notarization from environment and sealed DMGs in `postMake`. Task requires unsigned AgentLab builds. Remove those live signing paths while preserving zip, dmg, native runtime checks, branded IDs and feed coordinates. Add an authoritative Hosted policy above the historical upstream runbook. Keep exactly one publisher, provenance/checksums/feed completeness, unsigned platform warning expectations and full macOS zip updates. No release was run.
- Em/en punctuation in patched content was replaced per workspace instructions; updater text expectations were updated together.

## Preserved clean boundaries

- Preload composes upstream profiles/history/consent/shell APIs with downstream paired-machine APIs. It needs no manual patch because its textual merge is clean.
- CSP keeps arbitrary HTTPS/WSS selected-machine targets while retaining upstream additions. No listener, gateway bind or loopback auth change is included.
- Paired-machine certificate verification remains installed before opening the first window. Cookie staging, generation checks, encrypted passcode storage and selected-machine lifecycle are unchanged.
- REST still attaches an explicit bearer, refuses absent credentials, bounds requests and retries authentication once. SSE and WSS keep cookie credentials; native browser runtime registration and terminal theme transport retain their selected-machine restrictions.
- Upstream Windows shell probing and terminal preferences compose with downstream shell cancellation and machine-aware clients.
- The generated schema is never included in a patch. Package manifests/locks and upstream-only dependencies are the owner's ordinary merge result, not reconstructed here.

## Verification and remaining owner work

See `VERIFICATION.md` for commands, outcomes and limits. This candidate is not a release certification or an independent review.

Required owner integration checks:

1. Regenerate schema from merged DTO/spec sources, retaining Hosted optional `path` plus `cloneUrl`. Temporary typecheck currently reports only this expected mismatch.
2. Keep `/api/v1/agents/codex/accounts/events` and new credential/install controls local-only absent an explicit remote contract. Follow-up 04 supersedes the earlier account-SSE allowlist suggestion. Do not broaden gateway cookie authorization.
3. Ensure release workflows do not inject signing/notarization credentials and preserve unsigned artifact checks plus all updater manifests. The config and documentation guidance here is advisory; only the regression test is shipped, and workflow resolution is owned elsewhere.
4. Run packaged native SQLite/browser import and cross-platform Electron smoke verification. The native browser-host suite could not load Electron in the temporary install. Browser-profile IPC tests passed; this does not prove native runtime packaging or interactive browser behavior.

The external macOS browser-import policy conflict described below requires an owner decision. The other items above remain integration/verification obligations for the intake owner, not completed claims.


## Direct resolved-file handoff for PR #150

`resolved-frontend-files.tar.gz` contains 40 resolved files with repository-relative
paths. Owner can extract it over its true merge, bypassing the patch baseline
restore step. `resolved-deletions.txt` lists the one required deletion:
`frontend/src/renderer/hooks/useAgentsQuery.ts`. Delete that obsolete file and
stage the intended resolutions in the owner's existing merge. The archive
contains no Forge config, generated schema, workflow, AGENTS, or evidence docs.
All archive members were checked against `patch-manifest.json`; see
`archive-check.log`. Neither archive extraction nor deletion was performed in
the owner's worktree by this worker.

### Unresolved external browser-import policy conflict

Upstream `frontend/src/main/browser-profile-import.ts:131-206` defines macOS
source roots under `~/Library/Application Support` for Chrome, Edge, Brave,
Chromium, Vivaldi, Arc, Firefox and Zen. `discoverInternal` probes these roots;
`readSmallJSON` and directory enumeration read source data; the source SQLite
open at line 707 is readonly. This conflicts with the retained literal ban on
Application Support reads, even though these are third-party source profiles
rather than AO-owned state.

The candidate correctly places AO browser profile/history/staging outputs under
the Hosted root. That is not a policy exemption for external source reads.
The upstream import module is a clean upstream addition, is not part of the
archive/patches, and still requires an owner decision: disable macOS external
profile discovery/import, or explicitly narrow the policy to AO-owned state.
No exemption was assumed, and no such policy change was made. Native browser
profiles without external import can retain upstream behavior under either
choice. This conflict was reported to hosted-ao-90 and supersedes the earlier
statement that no unresolved choice remained.

## Mobile and gateway follow-up from hosted-ao-44

`03-mobile-no-cloudflared.patch` is a separate supplemental patch against the
original frozen inputs' clean per-file auto-merge. Apply it after the earlier
archive or patches. It changes only `ConnectMobileContent.tsx` and
`ConnectMobileModal.test.tsx`; these paths were not in the original archive.
Do not replace the earlier archive: its hashes and 40 files remain unchanged.

Base/downstream mobile settings exposed authenticated LAN enable, regenerate,
disable and password/QR behavior. Upstream added a cloudflared installer offer
and a POST to `/api/v1/mobile/remote-access` after installation. The follow-up
removes the production installer import/rendering and that mutation, including
its pending/error/reset hooks. Existing credential generation and QR payloads
remain. Unreferenced upstream installer helpers are not wired into production.

Gateway routing remains a path-only contract. Installer mutation protection is
POST-specific at its separate boundary. No method predicate is added to the
gateway API, and no backend gateway or production cloudflared wiring is changed
by this frontend patch. The owner was notified of the upstream clean-merge UI
hazard and this supplemental resolution.

Verification: patch application check passed against frozen clean auto-merge
snapshots. Mobile modal, telemetry and pairing payload suites: 3 passed,
38 tests passed and 6 skipped. See `mobile-followup-tests.log`. Skipped tests
were not treated as passed or unskipped. This focused result does not replace
the owner's authenticated LAN/gateway integration tests.


## Local-only credential/install controls, follow-up 04

Apply `04-local-only-controls.patch` after the original archive (or patches 01
and 02). It is independent of patch 03, which must also be applied to remove
cloudflared production wiring. Original archive and manifests remain unchanged;
the candidate now includes both supplemental overlays. Patch 04 modifies four
archive files and adds two regression test files. It excludes reserved paths.

Per-route decision, coordinated with hosted-ao-90:

| Route family | Upstream intent | Hosted decision |
| --- | --- | --- |
| `/api/v1/agents/codex/accounts` and descendants | Credential inventory, ensure, login terminals, logout/delete, login operation verify/cancel, reset credit and events | Local-only until explicitly contracted. Renderer REST rejects remote requests before bearer acquisition; account SSE is not opened remotely and an existing local stream closes on machine switch. |
| `/api/v1/agents/codex/account-switches` and descendants | Global credential switch and recovery | Local-only under the same remote request guard. |
| `/api/v1/agents/auth-plans`, `/installers`, `/install-jobs` | Credential and installation planning/status | Local-only. |
| `/api/v1/agents/{agent}/auth`, `/probe`, `/install`, `/verify` | Start credential terminal, probe authentication, install and verify | Local-only. |
| `POST /api/v1/system/install/{target}` | System installer mutation | Local-only POST-specific guard. GET status behavior is preserved, without changing the gateway path-only contract. |
| Ordinary project/session REST and `/api/v1/events` | Selected-machine work and CDC | Existing authenticated remote behavior remains. |

Global settings render a local-machine explanation instead of credential,
harness, cloud-control or mobile-control sections when a remote machine is
selected. All-sections mode omits those controls. The API guard also covers
non-settings callers, including stale controls during machine changes, using
the current base URL on each request. It returns 403 with
`local_machine_required` without fetching or asking for a remote bearer.

This renderer defense is not gateway authorization. The owner must enforce the
same local-only routing at the backend, retaining the gateway's path-only
contract and the separate POST-specific system installer guard. In particular,
the earlier recommendation to allow remote account SSE is withdrawn.

Verification: `local-controls-patch-check.log` confirms all six patch outputs
apply and match the candidate. `local-controls-tests.log`: five suites passed,
98 tests passed and 6 skipped. Tests verify local credential calls work,
remote credential/installer calls never fetch or acquire a bearer, GET system
installer status still carries remote authentication, ordinary CDC remains
authenticated, and local account SSE closes on switching to remote. Settings
controls unmount when machine selection changes. Typecheck is recorded in
`local-controls-typecheck.log`; generated-schema reconciliation remains owned
by hosted-ao-90.
