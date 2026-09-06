# Auth plans and readiness diagnostic boundary audit

Target source: bde87aa3508a62ddb7e3923c5aa8a61a3b6e546f. Source was archived into this worker's isolated fixture. Owner files, route policy, migrations and generated files were not edited. CodeGraph was unavailable for the owner tree, so the call chains below were traced directly in the target source.

## Findings requiring action

### Generic launch readiness can refresh Codex credentials

POST `/api/v1/agents/readiness/ensure` accepts `purpose:"launch"`; empty agentIds means all agents. This path is outside `/api/v1/agents/codex`, so blocking that prefix alone does not constrain it.

Actual chain:

1. `httpd/controllers/agents.go`: ensureReadiness passes request.AgentIDs and request.Purpose unchanged to Catalog.EnsureReadiness.
2. `service/agent/readiness.go:58`: EnsureReadiness calls readiness.Ensure. `readiness_coordinator.go:383,503`: runCheck reaches checkAuthentication for installed agents.
3. Production `service/agent/service.go:118` wires AuthenticationCheck to structuredCodexAuthentication.
4. `service/agent/codex_account_service.go:16`: launch waits for account bootstrap, then invokes ensureAuthentication with refreshToken=true. Display passes false.
5. `service/agent/codex_accounts.go:323,375`: ensureAuthentication runs runAuthentication. When refresh is true it acquires the account mutation lock, opens the account client, and calls Read(ctx, true).
6. `adapters/chatdriver/codexappserver/account.go:74,304`: Open starts a Codex app-server process; Read sends `account/read` with `refreshToken:true`.

This is more than cached diagnostics. It does not call the interactive login endpoint, but explicitly requests credential refresh and can invoke account bootstrap. Local launch paths intentionally use it. Do not globally disable this service behavior to fix remote diagnostics.

Minimal remote closure recommendation for owner90: deny the generic readiness/ensure route before remote auth/proxy, retaining cached GET readiness/list. A later display-only remote path would require explicitly separating its semantics; this intake does not invent one. Owner owns the LAN and both gateway handler tests. Targeted `/agents/codex/probe` is already covered by the full Codex prefix deny; the generic select-all ensure is the additional seam.

`service-boundary-evidence.patch` adds a real service/coordinator/structured-Codex path test with the external account client replaced by a recording fake. It demonstrates display reads with refresh=false, launch reads with refresh=true, and select-all behavior. It also proves subsequent cached readiness/list do not resolve binaries or reopen the account client, and serialized readiness excludes the account ID, email and binary path. This is evidence of privileged service behavior, not a remote allowlist test.

### Model diagnostics expose local paths, including from cache

Adjacent routes handled by the same AgentsController:

- GET `/api/v1/agents/{agent}/models`
- POST `/api/v1/agents/{agent}/models/refresh`, including revalidate=true

`service/agent/service.go:227` loadModels invokes the discoverer. `adapters/agent/modelcatalog/config.go:47,52` includes the full config path in file-read/parse errors. loadModels copies discoverErr.Error() into catalog.Warning in its fallback branches and sometimes persists it. Matching cache reads can return that warning later.

A real Qwen malformed-settings fixture reproduced the full local path in HTTP JSON. A separate persisted-warning case reproduced an arbitrary old private path and diagnostic token being forwarded. These fail before the patch.

`model-warning-sanitization.patch` replaces any nonempty model warning with fixed nonsecret wording at the HTTP serialization boundary. This covers both fresh discovery and historical cached warnings without rewriting stored records, changing DTOs, hiding the stale/refresh flags, or altering model selection. The patch is six source lines plus focused tests. The test uses real model discovery for malformed configuration and a cached-result fake for historical warning values. No generated output is needed because the response shape is unchanged.

## Routes that meet the inspected diagnostic boundary

| Surface | Real work | Wire result |
| --- | --- | --- |
| GET agents/auth-plans | Plans -> resolve -> agent Service.ResolveAgentBinary -> registered resolver | Fixed reviewed descriptions/docs/command names, availability and fixed missing-command reason. No resolved absolute path, raw resolver error or private terminal input. |
| GET agents/readiness | CachedReadiness -> Snapshot plus session-usage lookup | Installation/auth state, freshness, fixed reasons, usage count/time. No binary path/account details. |
| GET agents | List -> CachedReadiness -> projectInventory | Agent IDs/labels, coarse auth status, usage facts. No native check. |
| POST agents/refresh | Force display readiness | Bounded installation/auth diagnostics and cache updates, including Codex account/read refresh=false when structured account state exists. |
| POST agents/{agent}/probe | Invalidate authentication -> launch ensure | Coarse installed/auth result. Codex target is privileged refresh behavior and remains blocked by the existing Codex prefix. |

Plans is not a purely static constant lookup: it resolves executables to derive availability. Most resolvers use binaryutil PATH/candidate filesystem lookup. Muse validates the executable using `--version` with `MUSE_NO_AUTO_UPDATE=1`; it does not use login or install commands. The response's DisplayCommand is the fixed registry spelling, not the resolved private argv. Only Service.Start calls OpenCommandTerminal. The new Plans test supplies private resolver paths/errors and asserts that no terminal opens and descriptions remain registry-owned.

Readiness checks are not universally process-free: ordinary adapter AuthStatus implementations can call Claude `auth status`, Codex `login status`, Cursor `status`, Devin `auth status`, Kiro `whoami --format json`, OpenCode/Kilo `auth list`, Amp `usage --no-color`, and GitHub CLI `auth token` for Copilot. These results are classified internally into status enums; raw output is not sent through standard readiness responses. Other adapters read local credential/config presence. None of these source call sites invokes the registered install launcher or interactive auth Start method. This is source evidence about AO's commands, not a guarantee about every installed third-party binary's internals.

## Verification and remaining owner action

Service evidence tests passed with race detection: agent 3.213s, agentauth 1.865s. Real model-path and cached-warning regressions fail on target source before sanitization. See boundary-tests.log and model-before.log. The post-fix result is recorded in model-after.log when complete.

Owner must close the generic launch-readiness remote route and cover it with real LAN/hosted/pair handler tests. This worker supplies evidence and the separate model-warning patch; it does not overwrite the owner's classifier changes. The whole final API/model surface is not certified by this bounded audit.


## Owner resolution

The owner applied the fixed model warning and service evidence patches, denied the entire generic readiness/ensure prefix before auth/proxy in LAN and both gateways, and added real-handler coverage. Focused race verification passed across HTTP, gateway, agent/auth services, controllers and SQLite. This audit is bounded source evidence, not an independent final-head review.
