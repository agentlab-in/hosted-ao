> **Preserved from `/tmp/ao/review-batch-1-2_report.md`.** `/tmp` is ephemeral, so this is
> the only copy. Everything below the horizontal rule is the report exactly as the reviewer
> wrote it: no summarizing, no rewording, no reordering. The value is the file, the line, the
> failure scenario, and the smallest fix, and a summary would destroy all four.
>
> | | |
> | --- | --- |
> | Reviewed state | `develop` @ `193f6cc52` |
> | Date | 2026-07-30 |
> | Reviewer session | `hosted-ao-12` (AO worker, read-only) |
> | Findings | 17: 0 critical, 4 high, 5 medium, 8 low |
>
> **Where each finding was fixed.** Every finding was fixed. This mapping was added on
> import and is not part of the original report.
>
> | Finding | Fixed in | Issue |
> | --- | --- | --- |
> | H1 duplicate `Access-Control-Allow-Origin` | [#19](https://github.com/agentlab-in/hosted-ao/pull/19) | [#15](https://github.com/agentlab-in/hosted-ao/issues/15) |
> | H2 `publicUrl` fed to `autocert.HostWhitelist` as a hostname | [#19](https://github.com/agentlab-in/hosted-ao/pull/19) | [#15](https://github.com/agentlab-in/hosted-ao/issues/15) |
> | H3 SSE cannot authenticate through the gateway | [#19](https://github.com/agentlab-in/hosted-ao/pull/19) | [#15](https://github.com/agentlab-in/hosted-ao/issues/15) |
> | H4 `stale-if-error` never throttles and holds the mutex | [#19](https://github.com/agentlab-in/hosted-ao/pull/19) | [#15](https://github.com/agentlab-in/hosted-ao/issues/15) |
> | M1 schema comment claims `machines.hostname` is the audience | [#18](https://github.com/agentlab-in/hosted-ao/pull/18) | [#16](https://github.com/agentlab-in/hosted-ao/issues/16) |
> | M2 credential in `cloneUrl` persisted, served, and logged | [#20](https://github.com/agentlab-in/hosted-ao/pull/20) | [#17](https://github.com/agentlab-in/hosted-ao/issues/17) |
> | M3 control-plane `DATA_DIR` defaults to `./data` | [#18](https://github.com/agentlab-in/hosted-ao/pull/18) | [#16](https://github.com/agentlab-in/hosted-ao/issues/16) |
> | M4 `PUBLIC_ORIGIN` is not normalized | [#18](https://github.com/agentlab-in/hosted-ao/pull/18) | [#16](https://github.com/agentlab-in/hosted-ao/issues/16) |
> | M5 nothing tests the issuer against the verifier | [#22](https://github.com/agentlab-in/hosted-ao/pull/22) | [#21](https://github.com/agentlab-in/hosted-ao/issues/21) |
> | L1 `keys.Rotate` partial write | [#18](https://github.com/agentlab-in/hosted-ao/pull/18) | [#16](https://github.com/agentlab-in/hosted-ao/issues/16) |
> | L2 client-controlled `X-Real-IP` / `X-Forwarded-*` forwarded | [#19](https://github.com/agentlab-in/hosted-ao/pull/19) | [#15](https://github.com/agentlab-in/hosted-ao/issues/15) |
> | L3 preflight answered before the path allowlist | [#19](https://github.com/agentlab-in/hosted-ao/pull/19) | [#15](https://github.com/agentlab-in/hosted-ao/issues/15) |
> | L4 `/mux` accepts only the cookie | [#19](https://github.com/agentlab-in/hosted-ao/pull/19) | [#15](https://github.com/agentlab-in/hosted-ao/issues/15) |
> | L5 `ACCESS_TOKEN_TTL` is unbounded | [#18](https://github.com/agentlab-in/hosted-ao/pull/18) | [#16](https://github.com/agentlab-in/hosted-ao/issues/16) |
> | L6 `machine.json` does not honour `AO_DATA_DIR` | **not closed by #19.** Re-reported as **L-H** by the server-side review, then closed in [#43](https://github.com/agentlab-in/hosted-ao/pull/43) | [#39](https://github.com/agentlab-in/hosted-ao/issues/39) |
> | L7 data dir and database modes | [#18](https://github.com/agentlab-in/hosted-ao/pull/18) | [#16](https://github.com/agentlab-in/hosted-ao/issues/16) |
> | L8 rejection paths with no test | clone-flow half in [#20](https://github.com/agentlab-in/hosted-ao/pull/20), the three gateway gaps in [#19](https://github.com/agentlab-in/hosted-ao/pull/19) | [#17](https://github.com/agentlab-in/hosted-ao/issues/17), [#15](https://github.com/agentlab-in/hosted-ao/issues/15) |
>
> L6 is the one worth stating plainly: it was the single finding this review raised that the
> first round of fixes did not close. #43 closed it as a **documented decision rather than a
> relocation**: `machine.json` stays at `~/.ao/machine.json` because deriving it from
> `AO_DATA_DIR` would move the file the gateway reads without moving the file `ao setup-vm`
> writes. `DefaultMachineFilePath` was exported, given the reasoning in its doc comment, and
> pinned by a test. See the build log, [hosted-ao-v1-build-log.md](../hosted-ao-v1-build-log.md).

---

# Review: all merged Hosted AO v1 work (batches 1 and 2)

Reviewer: AO worker `hosted-ao-12`. Read-only. Nothing was changed.
Reviewed state: `develop` @ `193f6cc52`, commits `47454f320`, `dd7a248bf`, `dca252cd7`,
`3579380d8`, `8ef0cf91b`, `820a7a565`, `193f6cc52` (56 files).

## 1. Verdict

Yes, safe to build batch 3 (device flow, machine registry) on top of, provided M1 and M4
are fixed first, because batch 3 is exactly where they bite. Zero critical findings: the
token verifier itself is sound, signature-before-claims, alg-pinned, fail-closed.
Four high findings all land in batch 5 integration (gateway plus desktop), not in batch 3;
two of them (H1, H3) mean the gateway as merged cannot serve the Electron renderer at all.

## 2. Findings, ranked by severity

### Critical

None. Stated plainly rather than manufactured: I tried to break `VerifyToken` and could
not. See section 5 for what that means concretely.

### High

---

**H1. Duplicate `Access-Control-Allow-Origin` header breaks every browser request through
the gateway. CONFIRMED (empirically reproduced).**

`backend/internal/vmgateway/proxy.go:227-229` sets `Access-Control-Allow-Origin` and
`Access-Control-Allow-Credentials` on the response writer, then calls `next` (which is
eventually the reverse proxy). The daemon behind it sets the *same two headers* itself in
`backend/internal/httpd/cors.go:53-55`, because the forwarded request still carries
`Origin: app://renderer` and `app://renderer` is in `config.DefaultAllowedOrigins`
(`backend/internal/config/config.go:70-72`). `httputil.ReverseProxy` merges the upstream
response headers with `dst.Add`, not `dst.Set`, so both values survive.

Failure scenario: the renderer issues `GET /api/v1/projects` with a valid Bearer token and
`Origin: app://renderer`. Response contains
`Access-Control-Allow-Origin: app://renderer, app://renderer`. Chromium rejects it
("the 'Access-Control-Allow-Origin' header contains multiple values ... but only one is
allowed") and the fetch fails. This is every REST call from the desktop app, not an edge
case. I reproduced the header duplication with a standalone stdlib program modelling
`corsGate` plus `NewSingleHostReverseProxy` plus a CORS-setting upstream:

```
Access-Control-Allow-Origin values      = ["app://renderer" "app://renderer"]
Access-Control-Allow-Credentials values = ["true" "true"]
```

Why the tests miss it: the fake daemon in `proxy_test.go:36-42` sets only `X-From: daemon`,
never CORS headers, so `TestGateway_ValidToken_ProxiesToDaemon` cannot see this.

Smallest fix: in `newReverseProxy` (`proxy.go:63-78`), add a `ModifyResponse` that deletes
the upstream's CORS headers so only the gateway's own survive:

```go
proxy.ModifyResponse = func(res *http.Response) error {
    for _, h := range []string{
        "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials",
        "Access-Control-Allow-Methods", "Access-Control-Allow-Headers",
        "Access-Control-Max-Age", "Access-Control-Allow-Private-Network",
    } {
        res.Header.Del(h)
    }
    return nil
}
```

Also make the test's fake daemon emit CORS headers, or the fix is untested.

---

**H2. `machine.json`'s `publicUrl` is fed to `autocert.HostWhitelist` as if it were a bare
hostname, so the gateway will never obtain a certificate. CONFIRMED.**

`backend/internal/vmgateway/config.go:120` does
`cfg.Domain = firstNonEmpty(cfg.Domain, mf.PublicURL)`, and `Domain` goes straight to
`autocert.HostWhitelist(cfg.Domain)` at `backend/internal/vmgateway/server.go:46`. The
field is named and documented as a URL: `PublicURL string \`json:"publicUrl"\`` at
`machinefile.go:19`; the spec says `setup-vm` writes "public URL"
(`docs/superpowers/specs/2026-07-29-hosted-ao-v1-accounts-and-machines.md:139-140`) and the
schema calls `machines.hostname` "the machine's public URL"
(`controlplane/internal/storage/sqlite/migrations/0001_init.sql:15`).

Failure scenario: `setup-vm` writes `{"publicUrl": "https://vm.example.com", ...}`.
`HostWhitelist` runs `idna.Lookup.ToASCII("https://vm.example.com")`, which errors on the
`:` and `/`, and per its own doc comment "invalid hosts will be silently ignored"
(`golang.org/x/crypto@v0.51.0/acme/autocert/autocert.go:68-88`). The whitelist is therefore
**empty**, so `HostPolicy` rejects every SNI name, no certificate is ever issued, and every
TLS handshake on :443 fails. The gateway starts and logs success; nothing detects this until
a real client connects. `--domain vm.example.com` on the command line works fine, which is
why the tests pass: `config_test.go` only ever supplies bare hostnames.

Smallest fix: normalize in `Resolve` and validate. After line 120:

```go
if cfg.Domain != "" && strings.Contains(cfg.Domain, "://") {
    u, err := url.Parse(cfg.Domain)
    if err != nil || u.Hostname() == "" {
        return Config{}, fmt.Errorf("invalid public url %q in %s", cfg.Domain, machineFilePath)
    }
    cfg.Domain = u.Hostname()
}
if strings.ContainsAny(cfg.Domain, ":/") {
    return Config{}, fmt.Errorf("domain %q must be a bare hostname", cfg.Domain)
}
```

See section 4 for the contract wording the `setup-vm` worker needs.

---

**H3. SSE cannot authenticate through the gateway: the renderer uses `EventSource`, which
cannot set an `Authorization` header, and the gateway accepts the cookie only on `/mux`.
CONFIRMED.**

`backend/internal/vmgateway/proxy.go:169-187`: `extractToken` returns the
`ao_gw_token` cookie only when `r.URL.Path == muxPath`; every other path requires
`Authorization: Bearer`. `/api/v1/events` is proxyable (`isProxyablePath` allows all of
`/api/v1` except `mobile` and `dev`) and is the daemon's SSE route
(`backend/internal/httpd/events.go:36`). The renderer connects to it with the browser
`EventSource` API at `frontend/src/renderer/lib/event-transport.ts:85-86`:

```ts
source = new EventSource(`${baseUrl.replace(/\/+$/, "")}/api/v1/events`, {
    withCredentials: isRemoteDaemonBaseUrl(baseUrl),
});
```

`EventSource` has no header API at all. Note that the existing code already assumes
*cookie* credentials for a remote daemon (`withCredentials`), and `api-client.ts:192`
similarly sets `credentials: "include"` for remote REST. So the desktop side is built
around a cookie and the gateway is built around a header, with `/mux` the only overlap.

Failure scenario: desktop app points at `https://vm.example.com`. Every SSE connection is
rejected 401 by `requireToken`, `EventSource` retries forever, and the app has no live
session events. There is no workaround on the client: the API simply cannot send the header.

`controlplane/TOKEN_CONTRACT.md:14-15` ("`Authorization: Bearer <jwt>` for REST and SSE")
is the root of the disagreement; it is not achievable with the transport that exists.

Smallest fix: accept the cookie as a fallback on safe, non-state-changing GET routes, and
amend the contract. In `extractToken`:

```go
if r.URL.Path == muxPath || (r.Method == http.MethodGet && r.URL.Path == "/api/v1/events") {
    if c, err := r.Cookie(gatewayCookieName); err == nil && c.Value != "" {
        return c.Value, true
    }
}
```

Keep header-only for everything else on purpose: the cookie is ambient, so widening it to
POST/PATCH/DELETE would make `corsGate` the sole CSRF defence. Then update
`TOKEN_CONTRACT.md`'s transport line to say the cookie also carries SSE.

---

**H4. `stale-if-error` never throttles retries and holds the mutex across the network
fetch, so a control-plane outage serializes every gateway request behind a 10s timeout.
CONFIRMED.**

`backend/internal/vmgateway/jwks.go:112-130`. `Get` takes `c.mu` with a `defer` unlock and
then calls `c.fetch(ctx)` **while still holding it**. On the failure path
(`jwks.go:121-125`) it returns the stale keyset but leaves `c.fetchedAt` untouched, so the
staleness check at line 116 fails again on the very next request and a fresh fetch is
attempted every single time.

Failure scenario: the control plane is down (deploy, restart, network blip). The JWKS
client timeout is 10s (`jwks.go:104`). Request 1 enters `Get`, blocks 10s holding `c.mu`,
returns stale keys. Requests 2..N queue on the mutex. With 20 concurrent requests, the last
one waits about 200 seconds. The desktop app's own timeouts fire and the user is
disconnected, which is precisely the outcome the stale-if-error clause of
`TOKEN_CONTRACT.md:21-22` exists to prevent. The same serialization also applies once per
hour on the normal expiry path, but there it costs one request, not all of them.

`TestJWKSCache_StaleIfError` (`jwks_test.go:110-141`) passes because it makes one request
and asserts the keys come back, never that the failing fetch is not repeated.

Smallest fix: record the failed attempt so it backs off, and do not hold the lock across
the network call. Minimal version, keeping the current structure:

```go
const failureBackoff = 30 * time.Second

fresh, err := c.fetch(ctx)
if err != nil {
    if c.keys != nil {
        c.fetchedAt = c.now().Add(failureBackoff - c.ttl) // retry in failureBackoff, not immediately
        return c.keys, nil
    }
    return nil, err
}
```

Better, if the fix worker has room: use a `singleflight`-style guard so only one goroutine
fetches and the rest immediately get the stale keyset.

### Medium

---

**M1. The schema comment says `machines.hostname` (the public URL) is the JWT audience,
contradicting `TOKEN_CONTRACT.md` and both implementations. CONFIRMED.**

`controlplane/internal/storage/sqlite/migrations/0001_init.sql:14-17`:

> "machines holds one row per VM bound to an account via ao setup-vm. hostname is the
> machine's public URL, **used as both the JWT audience** and the reverse-proxy target."

But `TOKEN_CONTRACT.md:11` says `aud` = machine id; the issuer sets
`Aud: machineID` (`controlplane/internal/tokens/access.go:63`); the verifier compares `aud`
against `cfg.MachineID`, which comes from `machine.json`'s `machineId`
(`backend/internal/vmgateway/token.go:147`, `config.go:121`).

Failure scenario: the batch-3 or batch-4 worker who writes the token-issuance endpoint reads
the schema comment (the natural place to look when selecting from `machines`) and calls
`IssueAccessToken(accountID, m.Hostname)`. The gateway then returns `ErrAudienceMismatch`
for every request, 401 on everything, and the cause is invisible from either side. This is a
comment, so it is individually trivial and exactly the kind of thing that compounds.

Smallest fix: change the comment to "hostname is the machine's public URL, used as the
reverse-proxy target. The JWT audience is `machines.id`, not this column."

---

**M2. A credential embedded in `cloneUrl` is persisted to the projects DB, returned by the
projects API, and written to a daemon log. CONFIRMED.**

`backend/internal/service/project/clone.go:53` passes `cloneURL` verbatim to `git clone`,
so git stores it verbatim as `origin`. After the clone, `service.go:254` and
`service.go:282` set `row.RepoOriginURL = resolveGitOriginURL(path)`, which shells out to
`git remote get-url origin` (`service.go:682-688`). That value is returned as
`Project.Repo` with `json:"repo"` (`backend/internal/service/project/types.go:22`) from
`GET /api/v1/projects/{id}`, and is logged at
`backend/internal/observe/trackerintake/observer.go:175`
(`o.logger.Warn(..., "origin", project.RepoOriginURL)`).

Failure scenario: the client POSTs
`{"cloneUrl": "https://x-access-token:ghp_REDACTED@github.com/owner/repo.git"}`, which is
the natural non-interactive form and is effectively invited by `GIT_TERMINAL_PROMPT=0`
(`clone.go:55`). The PAT is now in `projects.repo_origin_url` on disk, in every projects API
response, and in journald on the hosted VM indefinitely. The sink predates `#6`; `#6` is the
first path that lets a client put a credentialed URL into it over HTTP.

Smallest fix: strip userinfo before persisting. In `cloneRepository`, or right before the
two `RepoOriginURL` assignments:

```go
if u, err := url.Parse(origin); err == nil && u.User != nil {
    u.User = nil
    origin = u.String()
}
```

Note the error path is clean: `classifyCloneError` (`clone.go:74-96`) never returns raw git
output, and `service_test.go:1005-1007` asserts that. Priority 6's requirement is met.

---

**M3. The control plane's default `DATA_DIR` is `./data`, relative to the process working
directory, and the EdDSA signing keys live inside it. CONFIRMED.**

`controlplane/internal/config/config.go:23` (`DefaultDataDir = "./data"`),
`controlplane/internal/keys/keys.go:49` (`filepath.Join(dataDir, "keys")`).

Failure scenario: a systemd unit without `WorkingDirectory=` runs with cwd `/`. Best case
`keys.Load` fails on `MkdirAll("/data/keys")` and the service refuses to boot, which is
loud and fine. Worse case, on a box where that path is writable, or after someone changes
`WorkingDirectory`, `loadOrGenerate` finds no `active.json`, silently **generates a new key
pair**, and starts signing with a `kid` no verifier has. Every VM's JWKS cache holds the old
keys for up to an hour (`jwks.go:106`), so every token is rejected until the cache expires,
and the `refresh_tokens` rows are in a SQLite file at the old path, so every desktop install
must sign in again. Recovery requires noticing that a directory moved.

Smallest fix: make `DATA_DIR` required (same treatment as the Google credentials at
`config.go:99-107`), or default to an absolute path, and set `DATA_DIR=` plus
`WorkingDirectory=` explicitly in the deployment unit. Either way, log the resolved absolute
data dir and the active `kid` at boot so a silent regeneration is visible.

---

**M4. `PUBLIC_ORIGIN` is not normalized, so a trailing slash yields an `iss` the gateway
will reject. CONFIRMED (latent: no wiring exists yet).**

`controlplane/internal/config/config.go:87-89` stores `PUBLIC_ORIGIN` raw.
`controlplane/internal/auth/auth.go:60` defensively trims it
(`strings.TrimRight(cfg.PublicOrigin, "/")`) for the redirect URI, which shows the author
expected a trailing slash in the wild. The issuer takes `issuerURL` verbatim into the `iss`
claim (`tokens/access.go:41,61`). The gateway pins `iss` to
`"https://ao.agentlab.in"` (`backend/internal/vmgateway/config.go:23`) and compares with
`!=` (`token.go:144`).

Failure scenario: `PUBLIC_ORIGIN=https://ao.agentlab.in/`. OAuth login works (the trim at
`auth.go:60` saves it), and every access token then carries
`iss: "https://ao.agentlab.in/"`. Every request to every VM fails with
`ErrIssuerMismatch`, and the gateway logs only "token rejected", with no way to see the
extra byte. Nothing currently calls `NewIssuer` (confirmed: zero non-test callers), so this
is latent, and it is cheapest to close before the issuance endpoint is written.

Smallest fix: normalize once in `config.Load`, at line 88:
`cfg.PublicOrigin = strings.TrimRight(raw, "/")`, and drop the now-redundant trim in
`auth.go:60`.

---

**M5. Nothing tests the issuer against the verifier, and nothing can, because they are
separate Go modules. CONFIRMED (structural).**

`controlplane/` and `backend/` are separate modules with separate CI workflows
(`.github/workflows/controlplane-go.yml`, `.github/workflows/go.yml`). Each side tests
itself against its own hand-written fixtures: `tokens_test.go:75-122` asserts the claims
against literals it wrote, `token_test.go` verifies tokens signed by
`signToken` (a test helper in the same package), `jwks_test.go:29-39` parses a JWKS the test
itself constructs. No test ever feeds a control-plane-produced artifact to the gateway.

Failure scenario: any future change to either side's encoding (padding, claim name, `kid`
derivation, `x` encoding) passes both CI workflows green and fails only on a real VM in
batch 5. This is the exact risk this review was commissioned to cover, and it is the one
finding whose fix has lasting value.

Smallest fix: commit a golden pair into `backend/internal/vmgateway/testdata/`: a
`jwks.json` produced by `keys.Manager.JWKS()` and an `access_token.jwt` produced by
`Issuer.IssueAccessToken` with a far-future `exp`. Add one `vmgateway` test that runs
`parseKeySet(jwks.json)` then `VerifyToken(token, ks, opts)` and requires success. Add the
mirror test in `controlplane/internal/keys` asserting the serialized JWKS field set still
matches the fixture, so regenerating the fixture is a deliberate act.

### Low

**L1. `keys.Rotate` partial write leaves `active` and `next` identical after a restart.
CONFIRMED.** `controlplane/internal/keys/keys.go:135-154` writes `active.json` (the promoted
key) first and `next.json` second, updating memory only after both succeed. If the second
write fails (disk full, EPERM), on-disk `active.json` holds the promoted key while
in-memory `m.active` still holds the old one. After a restart, `Load` reads
`active.json` == the promoted key and `next.json` == the *same* promoted key, so `JWKS()`
publishes a duplicate `kid` and the rotation slot is gone. Nothing calls `Rotate` yet
(confirmed: zero non-test callers). Fix: write both to temp files and rename, or write
`next.json` first, and update memory only after both succeed.

**L2. The gateway forwards client-controlled `X-Real-IP`, `X-Forwarded-*`, and the inbound
`Host` into a daemon that trusts all three. CONFIRMED, not currently exploitable.**
`proxy.go:80-93` strips only `Authorization` and `ao_gw_token`. `httputil.ReverseProxy`
appends to any existing `X-Forwarded-For` and passes `X-Real-IP` through untouched, and it
preserves the inbound `Host`. The daemon runs `middleware.RealIP`
(`backend/internal/httpd/router.go:52`) and gates its control routes with
`localControlRequest`, which trusts `r.Host` (`router.go:289-300`), a mechanism
`lan_listener.go:44-51` explicitly documents as spoofable. Today every route it gates
(`/shutdown`, `/internal/telemetry/*`) is outside the gateway's `/api/v1` plus `/mux`
allowlist, and chi does not use `middleware.CleanPath` (verified), so
`/api/v1/../shutdown` 404s at the daemon rather than normalizing onto `/shutdown`. So the
allowlist is currently the only thing standing between a spoofed `Host: 127.0.0.1` and a
loopback-only route. Fix: in the director, `r.Header.Del("X-Real-IP")` and
`r.Header.Del("X-Forwarded-For")` before `ReverseProxy` re-adds the true peer address. Do
**not** rewrite `r.Host` to the daemon address; leaving it as the public domain is what makes
`localControlRequest` correctly refuse.

**L3. CORS preflight is answered before the path allowlist, confirming that blocked routes
exist. CONFIRMED.** `corsGate` is outermost (`proxy.go:51-55`) and its preflight branch
returns 204 at `proxy.go:231-239` without consulting `isProxyablePath`. So
`OPTIONS /api/v1/mobile/status` with `Origin: app://renderer` and
`Access-Control-Request-Method: GET` returns 204, while the same path on any real method
returns 404. Information disclosure only; no request body is proxied. Fix: check
`isProxyablePath(r.URL.Path)` inside the preflight branch and `notFoundJSON` if false.

**L4. `/mux` accepts only the cookie, never a Bearer header. CONFIRMED.**
`proxy.go:170-176` returns early for `muxPath` and never looks at `Authorization`;
`TestGateway_Mux_UsesCookieNotHeader` asserts this deliberately. `TOKEN_CONTRACT.md:14-17`
says only that browsers *cannot* use a header, not that others *may not*. Consequence: the
CLI, a test harness, or any non-browser client cannot open the terminal mux through the
gateway. Fix: on `/mux`, try the cookie first and fall back to the header.

**L5. `ACCESS_TOKEN_TTL` is unbounded. CONFIRMED.**
`controlplane/internal/config/config.go:91-97` accepts any parseable duration. The spec
allows 10 to 30 minutes (`config.go:27-29` restates this). Nothing anywhere checks an access
token against a revocation list, so the TTL *is* the entire revocation window. A typo of
`720h` issues month-long unrevocable tokens and no test or boot check objects. Fix: validate
into `[10m, 30m]` in `Load` and fail loudly outside it.

**L6. `machine.json` does not honour `AO_DATA_DIR`, while the gateway's cert dir does.
CONFIRMED.** `backend/internal/vmgateway/config.go:160-166` resolves the default machine
file from `os.UserHomeDir()` plus `.ao/machine.json`, whereas `CertDir` derives from the
resolved `dataDir` (`config.go:132`), which honours `AO_DATA_DIR`. So
`AO_DATA_DIR=/srv/ao` moves the certificates but not `machine.json`; only
`AO_MACHINE_FILE` moves that. Not a hard-rule violation (nothing lands in an OS-default
app-data location), but a real inconsistency for the `setup-vm` worker to trip over. Fix:
either derive the default from `dataDir`, or state the split explicitly in the flag help.

**L7. Control-plane data directory and database are mode 0750 / driver default while the
keys subdirectory is 0700. CONFIRMED.** `controlplane/internal/storage/sqlite/db.go:39`
and `controlplane/internal/auth/session.go:47` both `MkdirAll(dataDir, 0o750)`;
`keys.Load` uses `0o700` for its own subdir (`keys.go:50`). `controlplane.db` is created by
the driver at the process umask default and holds Google subjects, emails, and refresh-token
hashes; `session_key` is correctly 0600 (`session.go:55`). Fix: use `0o700` for the data dir
too, since everything in it is sensitive.

**L8. Rejection paths with no test.** For the clone flow (`service_test.go:952-1031` covers
the happy path, `CLONE_AUTH_FAILED`, `CLONE_URL_INVALID`, `PATH_AND_CLONE_URL_CONFLICT`,
`CLONE_DESTINATION_EXISTS`): `CLONE_TIMEOUT`, `CLONE_HOST_NOT_FOUND`,
`CLONE_HOST_KEY_UNVERIFIED`, `CLONE_REPO_NOT_FOUND`, the `CLONE_FAILED` default,
`CLONE_NOT_CONFIGURED`, `CLONE_DEST_UNAVAILABLE`, scp-style URL parsing, and
`parseCloneURL`/`sanitizeDirComponent` against adversarial owner/repo segments all have no
direct test. For the gateway: no test that the upstream's CORS headers are not duplicated
(H1), no test that a failing JWKS fetch is not retried per request (H4), and no test that
`Resolve` rejects a `publicUrl` that is a URL rather than a hostname (H2). All three of my
high gateway findings are invisible to the current suite.

### Not a finding, recorded so it is not re-investigated

`go test ./internal/cli/` has 5 pre-existing failures on `develop`
(`TestSpawnCommand_MissingProjectContext`, `TestSpawnResolvesProjectFromAOSessionID`,
`TestSpawnAOSessionIDFailureRequiresProject`, `TestSpawnResolvesProjectFromCWD`,
`TestSpawnDefaultsToScratchWhenOnlyActiveProject`), all "daemon returned HTTP 404" because
`ao spawn` calls `GET /api/v1/agents` (`internal/cli/spawn.go:190`) against a stub that only
serves `/api/v1/projects`. Not caused by this work: none of the seven commits touch
`internal/cli/spawn.go`, `spawn_test.go`, or the agents route (verified with
`git diff --name-only 47454f320~1..HEAD`). They reproduce with an isolated
`AO_DATA_DIR`/`AO_RUN_FILE`, so they are not an artifact of my session's live daemon either.

## 3. Contract discrepancies: issuer vs verifier vs `TOKEN_CONTRACT.md`

The core claim-by-claim comparison **matches**. Listed explicitly because "we checked and
they agree" is the answer this section exists to give:

| Item | Issuer (`tokens/access.go`, `keys/jwks.go`) | Verifier (`vmgateway/token.go`, `jwks.go`) | Contract | Verdict |
|---|---|---|---|---|
| `alg` | `"EdDSA"` (`access.go:83`) | requires exactly `"EdDSA"` (`token.go:95`) | EdDSA | match |
| `typ` | `"JWT"` (`access.go:83`) | not checked | unspecified | match (unchecked is fine) |
| `kid` | 16 hex chars, `sha256(pub)[:16]` (`keys.go:105-108`) | lookup hint only, falls back to all keys (`jwks.go:69-80`) | unspecified | match |
| `iss` | `i.issuer`, unwired (`access.go:61`) | pinned `https://ao.agentlab.in`, `!=` compare (`token.go:144`) | `https://ao.agentlab.in` | match, but see M4 |
| `sub` | `accountID` = `accounts.id`, a `uuid.NewString()` (`auth.go:90`) | `cfg.AccountID` from `machine.json.accountId` (`token.go:150`) | account id | match, both plain strings |
| `aud` | `machineID`, a single string (`access.go:63`) | `cfg.MachineID` from `machine.json.machineId`, single-string `!=` (`token.go:147`) | machine id | match, but see M1 |
| `exp` | `now + accessTTL`, Unix seconds (`access.go:64`) | `time.Unix(exp,0)`, rejects if `now > exp+skew` (`token.go:139-142`) | 15 min | match |
| `iat` | Unix seconds (`access.go:65`) | not checked | present | match (contract only requires it be minted) |
| `jti` | `uuid.NewString()` (`access.go:66`) | not checked | present | match |
| Encoding | `base64.RawURLEncoding` all three segments (`access.go:91-97`) | `base64.RawURLEncoding` all three (`token.go:81,99,123`) | JWT | match, no padding disagreement |
| JWKS `kty`/`crv` | `OKP` / `Ed25519` (`jwks.go:30-31`) | requires exactly `OKP` / `Ed25519`, skips others (`jwks.go:48`) | Ed25519 | match |
| JWKS `x` | `RawURLEncoding(pub)` (`jwks.go:33`) | `RawURLEncoding`, length-checked to 32 (`jwks.go:51-53`) | implied | match |
| JWKS `use`/`alg` | `"sig"` / `"EdDSA"` (`jwks.go:34-35`) | ignored | implied | match (ignoring is safe) |
| JWKS key count | active plus next (`jwks.go:25`) | any number, at least one usable (`jwks.go:57-59`) | active plus next slot | match |
| Skew | n/a | `DefaultSkew = 60s` (`token.go:14`), actually wired at `cli/vm.go:87` | 60s | match, wiring verified |
| JWKS cache | server sets `max-age=3600` (`keys/http.go:21`) | `ttl = time.Hour` (`jwks.go:106`) | 1 hour | match |
| stale-if-error | verifier-side per its own comment | present (`jwks.go:121-125`) | required | present but defective, see H4 |
| Fail closed | n/a | `Get` errors with nothing cached, `requireToken` 401s (`jwks.go:125`, `proxy.go:148-152`) | implied | match |

Remaining discrepancies, all already itemized above, gathered here because they compound:

1. **M1**, `0001_init.sql:15` says `machines.hostname` is the JWT audience. It is not.
2. **M4**, `PUBLIC_ORIGIN` is not normalized, so `iss` can differ from the pinned value by
   one trailing byte.
3. **H3**, `TOKEN_CONTRACT.md:14` promises Bearer-header transport for SSE, which the
   renderer's `EventSource` cannot do and the gateway requires anyway.
4. **H2**, the contract does not say whether `machine.json`'s `publicUrl` is an origin or a
   bare hostname; the reader needs a hostname and the spec and schema both say URL.
5. `TOKEN_CONTRACT.md:16-17` specifies the `/mux` cookie as "Secure, HttpOnly, host-only"
   but says nothing about `SameSite`. The renderer is a cross-site context
   (`app://renderer` reaching `https://vm.example.com`), so the cookie will need
   `SameSite=None; Secure` or the browser will not attach it to the WebSocket handshake at
   all. The contract should state this before the desktop worker guesses. Also note the
   cookie name is defined only in gateway code (`ao_gw_token`, `proxy.go:16`) and appears
   nowhere in `TOKEN_CONTRACT.md`; the desktop worker has to read Go source to find it.
6. `TOKEN_CONTRACT.md` does not state the refresh token's TTL or its rotate-on-use rule.
   The implementation picks 90 days (`refresh.go:21`) and rotates on every use
   (`refresh.go:61-118`), both reasonable, neither written down. Add them so the desktop
   worker does not re-derive.

## 4. The `machine.json` contract, for the future `setup-vm` worker

Reader: `backend/internal/vmgateway/machinefile.go`. Consumer: `Resolve` in
`backend/internal/vmgateway/config.go:95-158`.

Exact struct as merged (`machinefile.go:16-21`):

```go
type MachineFile struct {
    MachineID string    `json:"machineId"`
    AccountID string    `json:"accountId"`
    PublicURL string    `json:"publicUrl"`
    IssuedAt  time.Time `json:"issuedAt"`
}
```

So the file `setup-vm` must write is exactly:

```json
{
  "machineId": "3f2b1c8e-6d4a-4b7e-9c11-8a5f0e2d7b34",
  "accountId": "9a7d4e21-0b3c-4f5a-8e6d-1c2b3a4f5e6d",
  "publicUrl": "https://vm.example.com",
  "issuedAt": "2026-07-30T09:34:21Z"
}
```

Field by field:

- **`machineId`** (string, required unless `--machine-id` or `AO_VM_MACHINE_ID` is given).
  Becomes the expected `aud`, compared byte-for-byte and case-sensitively at
  `token.go:147`. Must be byte-identical to `machines.id` and to whatever the control plane
  puts in `aud`. That column is `TEXT PRIMARY KEY` (`0001_init.sql:19`) and the accounts
  precedent is `uuid.NewString()` (`auth.go:90`), i.e. a lowercase hyphenated UUID, so match
  that. It is **not** the hostname; see M1. The reader enforces no format: any non-empty
  string is accepted, so a mismatch fails silently at request time, not at startup.
- **`accountId`** (string, required unless overridden). Becomes the expected `sub`, compared
  byte-for-byte and case-sensitively at `token.go:150`. Must equal `accounts.id`, a
  `uuid.NewString()` value. Exactly one account per machine; the gateway has no concept of a
  multi-entry allowlist.
- **`publicUrl`** (string, required unless overridden). **This is the field to get right.**
  The reader assigns it straight to `Config.Domain` (`config.go:120`), which becomes
  `autocert.HostWhitelist(cfg.Domain)` (`server.go:46`) and is therefore matched against the
  TLS SNI server name. It must be a **bare DNS hostname**: `vm.example.com`, no scheme, no
  port, no trailing slash, no path. A full URL silently disables TLS entirely; see H2 for
  why. Two ways to resolve, pick one and write it into `TOKEN_CONTRACT.md` or the spec:
  - **(recommended)** Keep the field a full origin, since the spec, `machines.hostname`, and
    the desktop (which needs an origin to build a base URL) all want a URL, and fix the
    reader to extract the hostname with `url.Parse` plus `u.Hostname()`. Then `setup-vm`
    writes `"https://vm.example.com"`.
  - Or rename the field to `domain` and write a bare hostname. This diverges from the spec's
    wording and leaves the desktop to reconstruct the origin.
  Until this is decided, `setup-vm` should write a bare hostname, because that is what the
  code as merged actually requires.
- **`issuedAt`** (RFC 3339 timestamp). Parsed as `time.Time` by `encoding/json`, so it must
  be RFC 3339 (`2026-07-30T09:34:21Z`). **The gateway never reads this value**, but a
  malformed one fails the whole unmarshal (`machinefile.go:35`), which `Resolve` turns into
  a fatal error (`config.go:104-107`), so a bad timestamp prevents the gateway from starting
  over a field nobody consumes. Either emit valid RFC 3339 or make the reader tolerant.

Behavioural contract the reader guarantees:

- **A missing file is not an error.** `ReadMachineFile` returns `(nil, nil)` for
  `os.ErrNotExist` (`machinefile.go:28-30`); that is the "not bound yet" state. The gateway
  then starts only if flags or environment supply domain, machine id, and account id, and
  otherwise exits with the message at `config.go:145-151`.
- **A present but malformed file is fatal.** Any JSON error aborts startup.
- **Unknown fields are ignored.** No `DisallowUnknownFields`, so `setup-vm` may add fields
  (a name, a control-plane URL) without breaking the gateway.
- **Precedence is flag, then environment, then `machine.json`, then built-in default**
  (`config.go:116-123`). `setup-vm` cannot rely on the file winning.
- **Partial files are allowed.** Any subset of the three required values may come from the
  file; the rest must come from flags or environment.
- **Default path is `$HOME/.ao/machine.json`** via `os.UserHomeDir` (`config.go:160-166`),
  **not** the `AO_DATA_DIR`-resolved data dir. Only `AO_MACHINE_FILE` or `--machine-file`
  relocates it; see L6.
- **Mode 0600** is required by the spec
  (`docs/superpowers/specs/2026-07-29-hosted-ao-v1-accounts-and-machines.md:139-140`). The
  reader neither checks nor enforces it, so `setup-vm` owns that entirely.
- The gateway reads the file **once, at startup** (`Resolve` is called from
  `cli/vm.go:73`). Re-binding a machine requires restarting `ao vm serve`;
  `setup-vm` should say so, or restart the unit itself.

## 5. What I verified and found correct

Does not need re-reviewing.

**Token verification (`backend/internal/vmgateway/token.go`), read line by line:**
- Signature is verified **before** the payload is decoded at all. The payload is
  base64-decoded and unmarshalled at lines 123-130, strictly after the `verified` check at
  lines 111-119. No claim is read before its signature is checked.
- `alg` is pinned to `EdDSA` at line 95, before any key is consulted. `alg: none` and
  `HS256` are both rejected there. The Ed25519 public key is never reachable as an HMAC
  secret, because the only verification call in the package is `ed25519.Verify`
  (line 112). Tested at `token_test.go:87-119`.
- Signature length is checked against `ed25519.SignatureSize` (line 100) before use.
- `aud` **and** `sub` are both checked, not just `exp` (lines 147-152), and
  `VerifyOptions` refuses to run at all if any of issuer, audience, or subject is empty
  (lines 71-73), so a misconfigured gateway cannot accidentally skip a check.
- Missing `exp` is rejected rather than treated as "no expiry" (lines 132-134). A negative
  `Skew` is clamped to zero (lines 135-138).
- Fails closed: `ks == nil` or an empty key set returns `ErrBadSignature` (lines 103-105),
  and `requireToken` 401s when `jwks.Get` errors (`proxy.go:148-152`).
- No token material in logs or responses. `requireToken` logs only the sentinel error and
  the path (`proxy.go:155`); the raw token is passed straight to `VerifyToken` and never
  formatted into any message; `unauthorized` returns a fixed envelope
  (`proxy.go:189-192`). The wrapped `base64`/`json` errors carry offsets and type names,
  never token bytes.
- Credentials are stripped before proxying: `Authorization` and the `ao_gw_token` cookie
  are removed in the director, other cookies preserved (`proxy.go:80-93`), asserted at
  `proxy_test.go:252-255`.

**Loopback-only route blocking:** genuinely deny-by-default, not a blocklist. The allowlist
is `/mux` exactly, plus `/api/v1` and below minus `/api/v1/mobile` and `/api/v1/dev`
(`proxy.go:108-121`). `/shutdown` and `/internal/*` are excluded because they are not under
`/api/v1` at all, which is strictly stronger than the LAN listener's blocklist. Bypass
attempts I checked against the code rather than the test names:
- Percent-encoding cannot bypass it: `hasPathPrefix` matches `r.URL.Path`, which Go has
  already percent-decoded, so `/api/v1/%6dobile` is blocked, and `EscapedPath` preserves
  the original encoding on the way out.
- Casing: `/api/v1/Mobile` is allowed through the gateway but 404s at the daemon, because
  chi is case-sensitive and no case-insensitive router sits in front of it.
- `..` traversal: `/api/v1/../shutdown` is forwarded un-normalized, but the daemon does not
  use `middleware.CleanPath` (verified in `router.go:51-56`), so chi 404s it rather than
  routing onto `/shutdown`. Noted as L2 because the allowlist is what makes this safe.
- Trailing slash: `/mux/` is not `/mux`, so it 404s. `hasPathPrefix` matches only on segment
  boundaries, so `/api/v1/mobileapp` is correctly *not* caught by the `/api/v1/mobile`
  block (`proxy.go:128-130`, `proxy_test.go:258-280`).
- Deny returns 404, not 401/403, so a blocked route is not confirmed to exist
  (`proxy.go:96-105`).

**CORS:** a preflight is answered without a token, but no real request slips through. The
preflight branch requires both `OPTIONS` and a non-empty `Access-Control-Request-Method`
(`proxy.go:231`); `OPTIONS` without that header falls through to auth. `*` and the opaque
`null` origin are filtered out of the allowlist (`proxy.go:207-209`), and the wired value is
`config.DefaultAllowedOrigins` = `["app://renderer"]`, an origin web content cannot present.
A hostile page's cross-origin WebSocket to `/mux` carries an `Origin` and is 403'd before
auth, which is the right defence, since the cookie is ambient and browsers do not enforce
same-origin on WebSocket themselves.

**The `/mux` cookie is not a weaker path.** `extractToken` returns the cookie value and it
goes through the identical `VerifyToken` call as a Bearer header (`proxy.go:154`). Same
signature check, same `iss`/`aud`/`sub`/`exp` checks. The only asymmetry is L4 (the header is
not accepted on `/mux`), which is a narrowing, not a weakening.

**SSE and WebSocket plumbing:** `FlushInterval = -1` (`proxy.go:65`) is correct for both;
`httputil.ReverseProxy` handles the `Connection: Upgrade` hijack itself. Auth headers are
consumed at the gateway and not forwarded (see above). `ReadHeaderTimeout` is set on both
listeners (`server.go:56,62`) and no write timeout is set, which is required for long-lived
streams.

**Key and secret handling:**
- EdDSA private key files are 0600 and the directory 0700 (`keys.go:50,115`), asserted by
  `TestLoad_KeyFilesAreMode0600`. `JWKS()` publishes only the public half
  (`jwks.go:28-37`), asserted by `TestJWKS_PublishesActiveAndNextPublicKeysOnly`. No key
  material is tracked in git (`git ls-files controlplane` shows source only) and
  `/controlplane/data/` is gitignored (`.gitignore:31-34`). Nothing logs a private key.
- Refresh tokens are stored hashed, never in plaintext: 32 bytes from `crypto/rand`
  (`refresh.go:150-156`), SHA-256 hex on the way in (`refresh.go:158-161`), and the lookup
  is by hash (`refresh.go:79`). Plain SHA-256 is the right choice here, not bcrypt, because
  the input is 256 bits of CSPRNG output, not a password.
- **Rotation genuinely invalidates the old token.** I read this specifically because it is
  the classic silent failure. `RotateRefreshToken` (`refresh.go:66-118`) opens one
  transaction, refuses an already-revoked row (lines 88-90), refuses an expired row
  (lines 91-93), sets `revoked_at` on the presented row (lines 96-100), inserts the
  replacement (lines 106-112), and commits once (line 114). A second presentation of the old
  token hits the `revokedAt.Valid` branch and returns `ErrRefreshTokenRevoked`. The old
  token is dead. `token_hash` is `UNIQUE` (`0001_init.sql:54`), and a concurrent double-use
  contends on the write transaction rather than both succeeding.
- Google client id and secret come only from the environment, and `Load` refuses to boot
  without either (`config.go:99-107`). `controlplane/.env.example` has empty values for
  both and no other credential; nothing else in it is sensitive.
- PKCE uses `crypto/rand`: 32 bytes for the verifier, S256 challenge
  (`oauth.go:53-62`); `state` is 24 bytes from `crypto/rand` (`oauth.go:66-72`) and is
  validated against the signed flow cookie on callback, rejecting both empty and mismatched
  (`oauth.go:183-186`), tested at `oauth_test.go:267-295`. The flow cookie is HMAC-SHA256
  signed with a 32-byte key, compared with `hmac.Equal` (`session.go:89`), carries its own
  10 minute expiry, and is cleared before the callback branches (`oauth.go:178`), so a
  captured cookie cannot be replayed after use. `sanitizeNext` closes the open redirect by
  requiring a single leading `/` (`oauth.go:76-81`, tested). `id_token` is deliberately not
  parsed; the account is keyed on the userinfo `sub`, and both `sub` and `email` are required
  to be non-empty (`oauth.go:297-302`).
- The session cookie is `HttpOnly`, `SameSite=Lax`, host-only (no `Domain`), carries a
  30 day expiry both in the cookie and inside the signed payload, and is `Secure` whenever
  `PUBLIC_ORIGIN` is https (`session.go:100-108`, `auth.go:64`). It is not a guessable
  session id at all: it is `accountID|exp` plus an HMAC-SHA256 tag, so forging one requires
  the key, and the key file is 0600 (`session.go:55`). Tampered, wrong-key, and expired
  cookies are all rejected and all three are tested (`session_test.go:69-121`).
- The sign-in page is `html/template` with contextual escaping, `?error=` is mapped through
  a fixed switch rather than echoed (`pages.go:41-52`), and the only interpolated URL is
  built from `sanitizeNext` plus `url.QueryEscape`. No XSS vector found.

**Hard-rule compliance:**
- The daemon is untouched by `#14`: still `127.0.0.1`, still unauthenticated. Confirmed by
  file list, no file under `internal/httpd/` or `internal/daemon/` appears in `193f6cc52`
  except via the `vmgateway` package's own import of `envelope`.
- `ao vm serve` stays inside ADR 0002: separate process, separate systemd unit, :80 for
  ACME plus https redirect (nil fallback to `manager.HTTPHandler`, so nothing is ever served
  plaintext, `server.go:55`), :443 for TLS, token verified before proxying, control routes
  never proxied. `AGENTS.md:82-85` was amended with a narrow carve-out naming
  `ao vm serve` specifically, exactly as the ADR's Consequences section requires.
- `autocert.HostPolicy` is scoped to the single configured domain (`server.go:46`), so a
  crafted `Host` to the VM's bare IP cannot trigger a certificate request for someone
  else's name. `CertDir` resolves under the `AO_DATA_DIR`-honouring data dir
  (`config.go:127-133`, `cli/vm.go:73`), never an OS-default app-data location, and it is
  created before any socket is bound (`server.go:39`).
- Graceful shutdown drains both listeners and cannot leak either serve goroutine
  (`server.go:72-118`); the drain loop at lines 108-112 is correct.

**`cloneUrl` (`#6`):**
- The clone destination cannot escape `reposRoot`. `dest` is
  `filepath.Join(m.reposRoot, owner+"-"+repo)` (`clone.go:38`) where both components passed
  `safeDirComponent` = `^[A-Za-z0-9._-]+$` with explicit `.` and `..` rejection
  (`clone.go:115,154-160`). No `/` can survive, so `Join` cannot traverse. I traced the
  hostile cases: `https://host/../../etc/x.git` reduces to owner `etc`, repo `x`, because
  only the last two segments are used (`clone.go:143-150`); scp-style
  `git@host:a/b.git` parses correctly; absolute paths, empty owner, and empty repo are all
  rejected.
- Raw git output never reaches the client. `classifyCloneError` (`clone.go:74-96`) inspects
  the output only to choose a branch and returns a fixed remediation string in every branch
  including the default; `CLONE_TIMEOUT` likewise (`clone.go:64-67`). `service_test.go`
  asserts the message contains neither `fatal:` nor the test server URL. A partial clone is
  cleaned up so a retry is not blocked (`clone.go:62`).
- `~/.ao/repos` honours `AO_DATA_DIR`: `ReposRoot: filepath.Join(cfg.DataDir, "repos")`
  (`backend/internal/daemon/daemon.go:160`) with `cfg.DataDir` from `resolveDataDir()`
  (`backend/internal/config/config.go:324-331`), which reads `AO_DATA_DIR` first.
- One code path for local and remote: a single `Service.Add` and a single controller
  (`backend/internal/httpd/controllers/projects.go:53-68`) decoding into `AddInput`. No
  parallel remote implementation exists.
- `openapi.yaml`, the Go DTO, and the generated TS schema agree exactly: `cloneUrl` is an
  optional string in all three (`backend/internal/service/project/dto.go:20`,
  `backend/internal/httpd/apispec/openapi.yaml:2236`,
  `frontend/src/api/schema.ts:815`), and the documented 201/400/409/500 responses
  (`specgen/build.go:684-691`) match the codes actually returned.

**Cross-cutting:** no duplication or contradiction between `#12` and `#13` in
`cmd/controlplane/main.go`, `internal/server/server.go`, or `internal/config/`. They
compose cleanly: `server.New(db, km)` registers `/healthz` plus JWKS, then
`authSvc.Register(mux)` adds the three auth routes to the same mux, with no route collision
and no duplicated config keys. Both key stores (`keys/`, the auth `session_key`) sit under
the same `DataDir` with distinct filenames. The migration covers `device_codes` and
`machines` with the columns the next batches need, including the `status` CHECK constraint,
the `user_code` UNIQUE constraint, `last_polled_at` for interval enforcement, and
`revoked_at` on `machines`. Foreign keys are actually enforced (`_pragma=foreign_keys(ON)`,
`db.go:29`), which many SQLite setups forget. The one gap batch 3 will hit is that
`device_codes` has no index on `user_code`, but the UNIQUE constraint already provides one,
so there is nothing to fix.

Test suites as run by me: `controlplane` 44 passed across 7 packages;
`backend/internal/vmgateway` and `backend/internal/service/project` all passed. The 5
`internal/cli` failures are pre-existing and unrelated, see section 2.
