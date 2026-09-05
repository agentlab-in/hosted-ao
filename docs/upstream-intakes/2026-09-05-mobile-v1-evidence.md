# Frozen v1 production evidence and ruling reconciliation

## Conclusion

V1 was a production-wired pairing and reconnect path in frozen downstream `78df8602e8aa5a482da50d46ec8cf6d175d36535`. It was not merely a test helper. Helper91's observation that the current intake phone is v2-only is also correct: upstream commit `cabd008114e1daa47f56e65d945ea0b4ec0a667a` replaced those production callers during the intake.

Chosen patch: retain the existing v3 restoration of authenticated v1 single-endpoint compatibility and unavailable-gate upstream v2. Do not manufacture a new protocol or restore anonymous identity. No source edits were needed after this reconciliation. V3 patch SHA-256 remains `a6904e59115e58badc8ae3b39e628547d29f3ffeebdef52748ef8697d647daf3`.

## Direct frozen production chain

All following references use the exact frozen downstream commit, not the intake candidate or a surviving helper's tests.

| Surface | Frozen source evidence |
| --- | --- |
| Real desktop QR producer | `frontend/src/renderer/components/settings/ConnectMobileContent.tsx:25-26` creates v1 JSON with host/port/password/optional secure. The actual `StyledQRCode` at lines 419-421 invokes that helper with activeHost, activePort, status.password and secureActive. |
| Reachable phone route | `packages/mobile/app/_layout.tsx:109` registers the `pair` screen. `packages/mobile/app/(tabs)/settings.tsx:179` navigates to `/pair`. |
| Live scanner callback | `packages/mobile/app/pair.tsx:149` connects the camera's `onBarcodeScanned` to `onScan`. Line 78 calls `parsePairingPayload`; line 89 applies its result to the config and invokes verification. |
| Authenticated save sequence | `packages/mobile/app/pair.tsx:97-98` awaits `pingServer(target)` before `saveConfig(target)`. Failure goes to the error path and does not save. |
| Real API request | `packages/mobile/lib/api.ts:778-781` implements pingServer using GET `/api/v1/sessions`. `req` at line 299 includes `authHeaders(cfg)`. |
| Bearer credential | `packages/mobile/lib/config.ts:28-29` maps the supplied password to Authorization Bearer. No identity request is involved. |
| Existing server support | `backend/internal/httpd/controllers/sessions.go:160` registers GET `/sessions`. Frozen `backend/internal/httpd/auth.go:119-128` reads the bearer credential for application API requests. No anonymous identity compatibility is needed. |
| Startup reconnect | `packages/mobile/lib/store.tsx:167-175` loads the persisted single-server config, sets it as the active config and invokes reload at startup. Subsequent ordinary requests use its bearer password. |

The v1 parser/application helper `packages/mobile/lib/pairing.ts` is byte-identical between frozen downstream and the tested intake baseline. The patch reuses it unchanged. The frozen tree has no `packages/mobile/lib/pairFlow.ts` history; that flow arrived with the upstream replacement.

## Exact history transition

`cabd008114e1daa47f56e65d945ea0b4ec0a667a`, subject `feat(mobile): connect from any network by racing every advertised endpoint (#4615)`, changes `packages/mobile/app/pair.tsx` by:

- Removing imports of parsePairingPayload/applyPairingPayload and the single-config verification flow.
- Adding pairFromCode, parsePairingCode, saveHost, probeEndpoint and raceEndpoints.
- Replacing the active scanner's v1 parse with v2 parsing and explicit v1 outdated-desktop rejection.
- Introducing the identity race before service verification and host persistence.

Ancestry checks:

- `git merge-base --is-ancestor cabd008114e1daa47f56e65d945ea0b4ec0a667a 78df8602e8aa5a482da50d46ec8cf6d175d36535`: exit 1. The v2 replacement is not in frozen downstream.
- The same check against frozen upstream `2e0614fc2b64c44f5e62b5983f0ebbc03ff5a3e5`: exit 0.

Earlier frozen history includes `073419ce68fbdc8685941bcbde341317257f57b0` for the password-authenticated LAN bridge and `499d5adcc53145b49fc1656ae8500a563722752f` for Tailscale pairing. These further predate the v2 replacement; they are not being recreated as new features.

## Why restoration is bounded

V3 keeps the existing v1 QR bytes, authenticated application request and single-server storage format. It adds cancellation/redirect containment and explicit v2 gates, including shared config reads that could otherwise revive v2 credentials. It does not modify backend routes/authentication, manufacture an identity handshake, create a QR version, or activate tunnels.

V1 authenticates a user-selected endpoint directly. It has no hostId field and no identity-first guarantee. Preserve that distinction: the patch does not send a v2 credential before identity, and issues neither anonymous nor authenticated identity requests. All upstream v2 pairing/reconnect remains unavailable until a separate identity-first authenticated protocol is designed.

This establishes active production wiring and compatible client/server request shapes from source. It is not historical phone/simulator execution evidence or packaged-device acceptance. The current patch has passing mocked transport/storage coverage: 726 tests in 79 files, focused 83 tests, plus typecheck. Native-device acceptance remains outstanding.

## Owner application

Authoritative V3 was applied at owner base bde87aa3508a62ddb7e3923c5aa8a61a3b6e546f. All 13 output hashes match the helper manifest. Ordinary owner npm ci succeeded without policy changes; owner mobile tests passed 726/726 and typecheck passed. Native device acceptance was not run.
