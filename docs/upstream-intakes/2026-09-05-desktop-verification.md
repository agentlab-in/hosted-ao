# Focused verification

Scope revision: Forge config and release documentation were removed from delivery. Tests used the isolated candidate config; the owner must implement and verify its reserved Forge resolution. Patch application was rechecked for the revised 41-path delivery.

All commands ran against `intake-support/candidate/frontend` in hosted-ao-92. No actual daemon, gateway, release or owner checkout was modified.

## Passed

- `python3 intake-support/check-patches.py`: both plain patches apply without index operations; all 41 file/deletion results match the candidate SHA-256 manifest.
- Renderer integration recheck: 5 suites, 139 tests passed. Covers CloneRepositoryDialog, CreateProjectFlow, SessionsBoard, REST API client and CDC/account SSE transport. Log: `recheck.log`.
- Broad candidate run: 27 suites passed; 4 updater assertions initially failed solely because patched copy replaced prohibited dash punctuation. The corresponding expected messages were corrected and updater tests rerun successfully. Log: `verification-final.log` retains that intermediate run honestly.
- Final updater and cloud policy recheck: 2 suites, 78 tests passed. Includes new full-macOS-download checks on stable and feature channels, and Hosted cloud-auth rejection in an unpackaged loopback configuration. Log: `policy-tests.log`.
- Shell and transport seam run: 10 suites, 124 tests passed. Covers shell preferences, workspace/daemon status, interface transition, credentialed notification stream, TurnSettingsBar, query cancellation/cache behavior, discovery, menus and terminal themes. Log: `seam-tests.log`.
- Main/native-focused subset: 7 suites, 96 tests passed; browser-host suite separately failed to load because of missing Electron binary, as detailed below. Log: `native-tests.log`.
- State-root guard, telemetry data-dir, paired certificate/probe/transport/store, remote lifecycle, request deadlines, terminal mux, browser-profile IPC, settings navigation, session view/chat, new-task/Sidebar mocks and i18n coverage were exercised in the runs above.
- Eight translation dictionaries parse successfully. Structured three-way merging found zero same-key conflicting semantic edits (`translation-collisions.json`).

Counts from separate runs overlap and must not be summed.

## Limited or blocked

- Frontend `tsc --noEmit`: exactly one remaining diagnostic, in `_shell.tsx` at remote project POST. The temporary schema is the unchanged upstream generated file, which requires `path` and lacks the Hosted `cloneUrl` alternative. Do not fix this by adding a dummy path or editing the generated schema. Owner must regenerate from combined backend source. Log: `typecheck.log`.
- E2E TypeScript check passed (exit 0), recorded in `e2e-typecheck.log`. Playwright browser/e2e flows were not run.
- `browser-view-host.test.ts` could not import Electron: `Electron failed to install correctly`. Dependencies were installed with lifecycle scripts disabled. An explicit Electron installer invocation did not produce `path.txt` in this temporary install. No fake binary or test assertion bypass was used. Browser-profile IPC tests passed, but native host and profile-import SQLite runtime behavior still need owner verification with a complete Electron installation.
- No packaged app, full renderer build, real remote machine, WSS handshake, native certificate callback, or release pipeline was executed. Transport tests use the repository's fakes. No additional live network calls were added to tests.
- Package dependencies were installed only in the isolated candidate. npm 12's default git-dependency restriction initially rejected the already-locked Electron node-gyp dependency; installation succeeded with `npm ci --ignore-scripts --allow-git=all`. The package lock was not rewritten.

## Integration obligations

1. Owner regenerates `openapi.yaml` and `schema.ts` from merged source and reruns both frontend typechecks.
2. Keep new account SSE and credential/install controls local-only absent an explicit contract. Follow-up 04 withdraws the earlier account-SSE allowlist recommendation.
3. Owner verifies unsigned release workflows, zip/dmg/manifests, artifact completeness and native SQLite rebuilds on supported platforms.
4. Owner runs complete native browser-host/import and packaged terminal/browser smoke checks.

The frontend source patches and focused results are ready for the owner's single true merge. This is not the later independent review.

Additional handoff: resolved-file archive members match manifest hashes; see archive-check.log. External macOS browser import reads Application Support and conflicts with retained policy. This is an unresolved owner decision, not validated compliant behavior.

Mobile follow-up: `03-mobile-no-cloudflared.patch` applies against frozen clean
auto-merge snapshots. Mobile modal/telemetry/pairing payload checks report
3 suites passed, 38 tests passed, 6 skipped (`mobile-followup-tests.log`).
Original archive remains unchanged; apply this supplemental patch afterward.

Local-only controls follow-up: `04-local-only-controls.patch` applies after the original archive/patches. Six outputs checked against candidate. Five suites passed: 98 tests passed, 6 skipped (`local-controls-tests.log`). Renderer guards do not substitute for backend gateway authorization.
