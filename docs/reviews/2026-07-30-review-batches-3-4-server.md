> **Preserved from `/tmp/ao/review-server-side_report.md`.** `/tmp` is ephemeral, so this is
> the only copy. Everything below the horizontal rule is the report exactly as the reviewer
> wrote it: no summarizing, no rewording, no reordering. The value is the file, the line, the
> failure scenario, and the smallest fix, and a summary would destroy all four.
>
> | | |
> | --- | --- |
> | Reviewed state | `develop` @ `a7eb9c6b2` |
> | Date | 2026-07-30 |
> | Reviewer session | `hosted-ao-24` (AO worker, read-only) |
> | Findings | 15: 0 critical, 2 high, 5 medium, 8 low |
>
> **Where each finding was fixed.** This mapping was added on import and is not part of the
> original report. One finding, L-B, is not closed by any merged PR; it is recorded as an
> open gap rather than mapped to a PR that did not address it.
>
> | Finding | Fixed in | Issue |
> | --- | --- | --- |
> | H-A approval POST has no rate limiting | [#45](https://github.com/agentlab-in/hosted-ao/pull/45) | [#37](https://github.com/agentlab-in/hosted-ao/issues/37) |
> | H-B `POST /device/code` unauthenticated, unbounded, never swept | [#45](https://github.com/agentlab-in/hosted-ao/pull/45) | [#37](https://github.com/agentlab-in/hosted-ao/issues/37) |
> | M-A no CSRF token and no `Origin` check on the approval POST | [#45](https://github.com/agentlab-in/hosted-ao/pull/45) | [#37](https://github.com/agentlab-in/hosted-ao/issues/37) |
> | M-B concurrent refresh exchange returns 500, not `invalid_grant` | [#45](https://github.com/agentlab-in/hosted-ao/pull/45) | [#37](https://github.com/agentlab-in/hosted-ao/issues/37) |
> | M-C `GET /api/v1/doctor` publishes and costs too much | [#43](https://github.com/agentlab-in/hosted-ao/pull/43) | [#39](https://github.com/agentlab-in/hosted-ao/issues/39) |
> | M-D `redactHomePaths` silently becomes a no-op | [#43](https://github.com/agentlab-in/hosted-ao/pull/43) | [#39](https://github.com/agentlab-in/hosted-ao/issues/39) |
> | M-E `normalizePublicURL` accepts `http://` for any host | [#45](https://github.com/agentlab-in/hosted-ao/pull/45) | [#37](https://github.com/agentlab-in/hosted-ao/issues/37) |
> | L-A `corsGate` passes an originless request straight through | [#43](https://github.com/agentlab-in/hosted-ao/pull/43) | [#39](https://github.com/agentlab-in/hosted-ao/issues/39) |
> | L-B a port in `publicUrl` is dropped by the gateway, kept by the control plane | **not closed.** No merged PR addresses it, and it was not carried into #37 or #39 | none filed |
> | L-C `JWKSCache.fetch` runs on the triggering request's context | [#43](https://github.com/agentlab-in/hosted-ao/pull/43) | [#39](https://github.com/agentlab-in/hosted-ao/issues/39) |
> | L-D a `\|` in `next` breaks sign-in | [#45](https://github.com/agentlab-in/hosted-ao/pull/45) | [#37](https://github.com/agentlab-in/hosted-ao/issues/37) |
> | L-E the attempt limiter is per-account, in-memory, never evicts | [#45](https://github.com/agentlab-in/hosted-ao/pull/45) | [#37](https://github.com/agentlab-in/hosted-ao/issues/37) |
> | L-F `createDeviceCode` retries on any insert error | [#45](https://github.com/agentlab-in/hosted-ao/pull/45) | [#37](https://github.com/agentlab-in/hosted-ao/issues/37) |
> | L-G `parseClaudeAuthStatus` embeds the harness's raw output | [#43](https://github.com/agentlab-in/hosted-ao/pull/43) | [#39](https://github.com/agentlab-in/hosted-ao/issues/39) |
> | L-H the previous review's L6 is not closed | [#43](https://github.com/agentlab-in/hosted-ao/pull/43), as a documented decision rather than a relocation | [#39](https://github.com/agentlab-in/hosted-ao/issues/39) |
>
> Section 3 of this report, "Did the previous review's fixes close", is the record of which
> batch 1 and 2 findings were verified closed at `a7eb9c6b2`. Those do not need re-reviewing.

---

# Review: the server half of batches 3 and 4 (control plane, gateway, tokens, daemon HTTP)

Reviewer: AO worker `hosted-ao-24`. Read-only. Nothing was changed.
Reviewed state: `develop` @ `a7eb9c6b2`, commits `86d9772d2` (#18), `1d242dc60` (#19),
`793fc8dff` (#22), `ecaef6c8d` (#26), `a7eb9c6b2` (#36).
Test suites run: `controlplane` 96 passed across 9 packages; `backend`
`internal/vmgateway`, `internal/doctor`, `internal/httpd` 325 passed across 8 packages.

## 1. Verdict

Yes, safe to run on a public VM, with two caveats to fix first: the device-approval POST
has no rate limiting at all (the limiter the code claims sits on the wrong handler), and
`POST /device/code` is an unauthenticated unbounded permanent write. Zero critical
findings: the device flow's core invariants (CSPRNG codes, hashed device codes,
server-side expiry, single approval, session-validated POST, server-side poll interval)
all hold, and every one of the previous review's H1 to H4, M1, M3, M4, M5, L1 to L5, L7
fixes actually landed correctly.

## 2. Findings, ranked by severity

### Critical

None. I tried to break the device flow's state machine and its token audience split and
could not. What that means concretely is in section 5.

### High

---

**H-A. `POST /device/decision` approves a user code with no rate limiting, so the
enter-code limiter is bypassed by skipping the page it guards. CONFIRMED (code reading).**

`controlplane/internal/device/pages.go:76` is the only call to `s.attempts.allow`, and it
is inside `handleSubmitCode` (`POST /device`, which only renders the confirmation page).
`handleDecision` (`POST /device/decision`, `pages.go:116-163`) reads `user_code` straight
out of the POST form and calls `s.approve` with no limiter check and no server-side link
back to any earlier `POST /device`. Confirmed by grep: `s.attempts` appears in exactly one
non-test location.

`controlplane/internal/device/codes.go:142-150` states the intent that was missed:

> "attemptWindow and attemptsPerWindow bound how fast one signed-in account may guess user
> codes on the enter-code page. The device code endpoint needs no equivalent because a
> device code has 256 bits of entropy, but a user code has about 34, so the page is the
> oracle worth closing."

Two entry points reach the user code space; one got the limiter, and it is the one that
does not change state.

Failure scenario: an attacker signs in with any Google account (accounts are free), then
POSTs `/device/decision` with `user_code=<guess>&action=approve` as fast as they like. The
responses are fully distinguishable: 200 result page on a hit, 400 for unknown or expired
(`statusForCodeError`, `pages.go:167-176`), 409 for already used. On a hit the code is
approved and bound to the **attacker's** account, `registerMachine` inserts a `machines`
row under the attacker's account (`store.go:152`), and the legitimate VM's polling client
then receives `account_id` = attacker, `machine_id` = the attacker's machine, and an access
token with `sub` = attacker (`endpoints.go:145-159`). `ao setup-vm` writes those into
`machine.json`, so `ao vm serve` on the victim's VM starts accepting the attacker's tokens:
full gateway access to the victim's machine, including `/mux`.

Honest sizing: the space is 20^8 ≈ 2.6e10 and a code lives 15 minutes, so a single code is
unlikely to fall at realistic request rates through Caddy. The finding is that the only
control the design has against guessing does not run on the path where a guess is worth
anything, and the `deny` branch is the same unmetered primitive (a hit kills someone else's
setup silently, since `deny` renders the same result page whether or not it hit,
`pages.go:129-135`).

Smallest fix: move the limiter check so both handlers share it. In `handleDecision`,
immediately after `requireAccount` returns:

```go
if !s.attempts.allow(accountID) {
    s.renderEnterPage(w, http.StatusTooManyRequests, enterPageData{
        Error: "Too many attempts. Wait a minute and try again.",
    })
    return
}
```

Add a test mirroring `TestSubmitCode_UnknownCodeIsRejectedAndRateLimited`
(`flow_test.go:571`) against `/device/decision`; there is currently no test on that path at
all for rate limiting.

---

**H-B. `POST /device/code` is unauthenticated, unrate-limited, stores an unbounded
attacker-chosen `machine_name`, and no code path ever deletes a `device_codes` row.
CONFIRMED.**

- `controlplane/internal/device/endpoints.go:65-104`: no auth, no limiter. `machine_name`
  is `strings.TrimSpace(params.Get("machine_name"))` with no length check (`:77-80`).
- `controlplane/internal/api/api.go:152-170`: `ReadParams` caps a **JSON** body at 64 KiB
  (`http.MaxBytesReader`, `:155`) but the form branch (`:166-169`) is plain
  `r.ParseForm()`, so a urlencoded body is capped only by net/http's 10 MiB default.
- `controlplane/internal/device/store.go:47-71` inserts a row per request.
- `grep -rn "DELETE FROM device_codes" controlplane/` returns nothing. `markExpired`
  (`store.go:109-113`) only flips `status`; its doc comment says "nothing sweeps the
  table" (`store.go:26-29`), which is accurate.

Failure scenario: an internet attacker loops
`POST /device/code` with `public_url=vm.example.com&machine_name=<10 MiB of text>`. Each
request writes a permanent row of roughly that size into `controlplane.db`. A few hundred
requests is gigabytes. That file sits in the same directory as the EdDSA signing keys and
the `accounts`/`refresh_tokens` tables (`config.go:70-77`, `keys.go:49`), so when the disk
fills, sign-in (`upsertAccount`), refresh rotation, and key rotation all start failing.
Nothing recovers on its own because nothing deletes anything.

Mitigating context, stated so it is not over-weighted: Caddy fronts this service and could
be configured with a body limit and a rate limit. Nothing in this repo does.

Smallest fix, three lines in three places:

1. `endpoints.go`, after `:77`: reject a `machine_name` longer than, say, 128 runes.
2. `api.go:166`, form branch: `r.Body = http.MaxBytesReader(w, r.Body, 1<<16)` before
   `ParseForm`.
3. `store.go`, inside `createDeviceCode` before the insert loop:
   `_, _ = s.db.ExecContext(ctx, "DELETE FROM device_codes WHERE expires_at < ?", now)`.
   That is the whole sweep: rows are useless past `expires_at` and every read already
   rejects them.

### Medium

---

**M-A. No CSRF token and no Origin check on the approval POST. `SameSite=Lax` is the sole
defence, and it does not cover a same-site attacker. CONFIRMED.**

`controlplane/internal/device/templates/device_confirm.html` posts to `/device/decision`
with exactly one hidden field, `user_code`. `handleDecision` (`pages.go:116-163`) checks
only that a session exists. `grep -rni "referer|\"Origin\"|csrf" controlplane/` finds no
check anywhere in the service; the only hits are a doc comment and the OAuth `state`
value. The session cookie is `http.SameSiteLaxMode` (`auth/session.go:109`), which is what
`pages.go:113-115` cites as the defence.

`SameSite` is scoped to the registrable domain, not the origin. `DefaultPublicOrigin` is
`https://ao.agentlab.in` (`config.go:22`), so a page served from **any other host under
`agentlab.in`** is same-site and its cross-origin POST carries the session cookie.

Failure scenario: the attacker calls `POST /device/code` with
`public_url=https://evil.example.com&machine_name=Production VM`, keeps the returned
`device_code` and `user_code`, and hosts an auto-submitting form at any `*.agentlab.in`
origin (or anywhere, against a client that does not enforce SameSite). A signed-in operator
loads it. Their account approves the attacker's code. The attacker's polling client then
receives the **victim's** `account_id` and an access token with `sub` = victim
(`endpoints.go:152-159`), and a `machines` row named "Production VM" pointing at
`https://evil.example.com` appears in the victim's desktop machine list
(`listMachines`, `store.go:339-371`). If the operator ever picks it, the desktop points at
the attacker's host.

Smallest fix, one function, no new state, given the device Service holds `publicOrigin`
already (`device.go:70`): in `handleDecision`, before `ParseForm`,

```go
if o := r.Header.Get("Origin"); o != "" && o != s.publicOrigin {
    s.renderEnterPage(w, http.StatusForbidden, enterPageData{Error: "That request did not come from this page."})
    return
}
```

Browsers send `Origin` on every POST, so this closes the same-site case without touching
the templates. A real per-session CSRF token is stronger but needs the `sessions` interface
(`device.go:48-50`) extended to hand out a signing key, which is a larger change.

---

**M-B. Two concurrent exchanges of the same refresh token return HTTP 500 `server_error`
instead of `invalid_grant`. CONFIRMED (empirically reproduced).**

`controlplane/internal/tokens/refresh.go:66-118` opens a deferred transaction
(`BeginTx(ctx, nil)`), SELECTs the row, then UPDATEs it. SQLite does **not** invoke the
busy handler for a read-to-write upgrade whose snapshot has gone stale, so the
`busy_timeout(5000)` in `storage/sqlite/db.go:28` does not absorb it. The raw error is not
one of the three sentinels `api/api.go:79-87` maps to `invalid_grant`, so it falls into the
`default` branch and becomes a 500.

I reproduced the mechanism outside the repo with `modernc.org/sqlite v1.55.0`, the same four
pragmas, and the same statement sequence (deferred tx, SELECT the row, UPDATE it, INSERT the
replacement, commit). With 8 concurrent rotations of one row:

```
successes=1  cleanLosses=6  dirtyErrors=1
g5: DIRTY ERROR: database is locked (5) (SQLITE_BUSY)
```

The good news is in the same line: exactly one rotation commits, so there is no
double-rotation and no corruption. The bad news is the loser's error shape.

Failure scenario: the desktop fires two refreshes at once (two windows, or a retry that
overlaps its predecessor). One gets 200 with a rotated token. The other gets
`500 server_error`, which any sane client treats as transient and retries with the token it
presented, which the winner already revoked, so it now gets `invalid_grant` and forces the
user to sign in again, even though a perfectly valid rotated token exists in the other
window.

Smallest fix: take the write lock before the read, so the loser blocks (and `busy_timeout`
applies) instead of failing the upgrade. Add `&_txlock=immediate` to the DSN in
`db.go:27-30`, or, scoped to this one path, map a `SQLITE_BUSY` from `RotateRefreshToken`
onto `ErrInvalidRefreshToken` so the caller sees `invalid_grant`.

The same read-then-write-in-a-deferred-tx pattern is in `device.approve`
(`store.go:121-176`) and `device.poll` (`store.go:246-325`). There the symptom is milder:
two browser tabs approving the same code produce a 500 "internal error" instead of the
intended 409, and two overlapping polls produce a 500 instead of an RFC 8628 error code.
`ao setup-vm` polls single-threaded, so poll is unlikely to hit it in practice.

---

**M-C. `GET /api/v1/doctor` publishes the machine's GitHub identity and token scopes, exact
tool versions, and out-of-home binary paths, and runs six subprocesses plus a live GitHub
API call on every request. CONFIRMED.**

Enumerating what actually crosses the wire on a real Ubuntu VM, from `doctor.Run`
(`backend/internal/doctor/doctor.go:156-193`) through the projection in
`backend/internal/httpd/controllers/doctor.go:62-79`:

| Check | Message content | Why it matters to an attacker with a token |
|---|---|---|
| `github-token` | `"gh token valid for <github-login> (scopes: repo, workflow, read:org)"` (`doctor.go:566-571`), or which of `AO_GITHUB_TOKEN` / `GITHUB_TOKEN` holds it | The machine's GitHub account name and the exact capability of the credential sitting on it |
| `git` | `/usr/bin/git (version 2.43.0; supports worktrees)` (`doctor.go:387`) | Exact version for CVE targeting |
| `tmux` | `/usr/bin/tmux (tmux 3.4)` (`doctor.go:419`) | Same |
| `claude-code`, `codex` | path plus exact harness version (`doctor.go:488`) | Same |
| `ao-binary` | both the running executable and the `ao` on PATH (`doctor.go:345,349`) | Install layout |
| `claude-auth` | resolved claude path, `authMethod`, `apiProvider` (`doctor.go:240`) | Whether the harness runs on a subscription or a raw API key, and which provider |
| `config` | `runFile=... dataDir=... port=<daemon loopback port>` (`doctor.go:166`) | The loopback port to aim a local exploit at |
| `sqlite` | db path and exact byte size (`doctor.go:291`) | Rough activity volume |
| `hooks-log` | a full log line, which carries a session id (`cli/hooks.go:239`) | Session identifiers |
| `codex-launch-flags` | `FirstOutputLine` of codex's own error output (`doctor.go:517`) | Whatever codex printed |

Reachable by anyone holding a valid access token for the machine, and separately by any
Connect Mobile client on the LAN: `/api/v1/doctor` is not in `lanControlBlockedPrefixes`
(`backend/internal/httpd/lan_listener.go:52-57`), so only the mobile password gates it
there.

Side effects, per request, with no caching and no concurrency limit: `os.MkdirAll` on the
data dir plus a create/write/delete temp-file probe (`doctor.go:169`, `:303-322`), up to
six `exec` probes at 2s each, and one authenticated `GET https://api.github.com/user` using
the machine's real token (`doctor.go:530-540`). N concurrent requests multiply all of it
and burn the operator's own GitHub rate limit.

Smallest fix, in `checkGitHubToken` and the controller:
1. `doctor.go:571`: drop the login and the scope list from the PASS message, e.g.
   `fmt.Sprintf("%s token valid", source)`. Both are only useful locally, and `ao doctor`
   text output can keep them behind a flag if wanted.
2. Memoize the report in `DoctorController` behind a mutex with a short TTL (10s is enough)
   so a burst of requests runs the probes once.

---

**M-D. `redactHomePaths` silently becomes a no-op when `os.UserHomeDir()` fails, and it
only rewrites `home + separator`. CONFIRMED.**

`backend/internal/httpd/controllers/doctor.go:63`: `home, _ := os.UserHomeDir()`, error
discarded. `:91-98`: `if home == "" || home == sep { return message }`, unchanged.

Failure scenario: the daemon runs under a unit that sets `AO_DATA_DIR` but not `HOME`.
`config.Load` still succeeds (`resolveDataDir` short-circuits on `AO_DATA_DIR`,
`backend/internal/config/config.go:324-327`), so `doctor.Run` proceeds past its first
check, but `os.UserHomeDir` errors, `home` is `""`, and the only privacy control on this
route does nothing. Every home path goes out verbatim:
`/home/ubuntu/.local/bin/claude`, `/home/ubuntu/.ao/ao.db`, account name included. The
response looks completely normal; nothing signals the defence did not run.

Second, narrower gap: a message that is exactly the home directory with no trailing
separator is never rewritten, because the replacement key is `home+sep` (`:97`). That is
the `data-dir` check when `AO_DATA_DIR` is the home directory itself.

Smallest fix: resolve the home directory once at daemon start and treat a failure as a
reason to withhold `Message` rather than to send it unredacted; and add
`strings.ReplaceAll(message, home, "~")` as a second pass, or compare with
`strings.TrimSuffix` semantics.

---

**M-E. `normalizePublicURL` accepts `http://` for any host, not just loopback. CONFIRMED.**

`controlplane/internal/device/codes.go:121-123` accepts both `http` and `https`.
`codes_test.go:86` pins `http://127.0.0.1:8443` as valid, which shows the intent was a
local-development affordance, but nothing restricts the scheme to a loopback host.

Failure scenario: `public_url=http://vm.example.com` is accepted, stored in
`machines.hostname` (`store.go:217`), returned by the poll response
(`endpoints.go:158`) and by `GET /api/v1/machines` (`store.go:341`), and written into
`machine.json`. The desktop builds its base URL from that, so the `Authorization: Bearer`
header and the `ao_gw_token` cookie, which `TOKEN_CONTRACT.md:89` requires to be `Secure`,
travel in the clear. `ao vm serve` only ever listens on TLS
(`vmgateway/server.go:55-63`), so the operator gets a broken machine rather than a working
plaintext one, but the registration itself succeeded and the desktop will try.

Smallest fix, in `normalizePublicURL` after the scheme check:

```go
if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
    return "", fmt.Errorf("public_url %q must use https", raw)
}
```

### Low

**L-A. `corsGate` does nothing to a request with no `Origin` header, while the `/mux` and
SSE cookie is `SameSite=None`. CONFIRMED.** `backend/internal/vmgateway/proxy.go:275-279`
returns straight to `next` when `Origin` is empty. `cookieAuthAllowed` (`proxy.go:242-247`)
accepts the ambient cookie on `/mux` for any method and on `GET /api/v1/events`. Browsers
do not send `Origin` on a subresource or navigational GET, and `SameSite=None`
(`TOKEN_CONTRACT.md:89`) means the cookie is attached cross-site. So
`new Image().src = "https://vm.example.com/api/v1/events"` from a hostile page is an
authenticated, proxied SSE connection held open on the daemon. There is no disclosure (the
response is opaque, no CORS grant) and nothing state-changing, so the impact is connection
exhaustion. Fix: on the two cookie-authenticated routes only, require an `Origin` and run
it through the same allowlist. `EventSource` and the WebSocket handshake both send one;
non-browser clients use the header path anyway.

**L-B. A port in `publicUrl` is silently dropped on the gateway side but preserved on the
control-plane side. CONFIRMED.** `vmgateway/config.go:184-197`: `https://vm.example.com:8443`
reduces to `vm.example.com` (the port is lost with `u.Hostname()`), while a bare
`vm.example.com:8443` is a fatal error. `device/codes.go:130` keeps `u.Host`, port included.
So the control plane stores `https://vm.example.com:8443`, the desktop connects there, and
the gateway certificates `vm.example.com` and listens on `:443` unless `--https-addr` is
also set. Nothing checks the two agree. Fix: reject a port in `normalizeDomain` with a
message naming `--https-addr`, or carry it into `HTTPSAddr`.

**L-C. `JWKSCache.fetch` runs on the triggering request's context. CONFIRMED.**
`vmgateway/jwks.go:136` passes the caller's `ctx` straight to `c.fetch`. A client that
disconnects mid-refresh cancels a refresh the whole gateway shares. With a keyset already
cached that records a 30s `failureBackoff` (`jwks.go:146`) despite the control plane being
healthy; on a cold start it returns the error and fails that request closed. Fix:
`context.WithTimeout(context.Background(), c.client.Timeout)` for the fetch.

**L-D. A `|` in the `next` parameter breaks sign-in, and `#26` added a new caller that
builds `next` from an attacker-influenced URL. CONFIRMED.**
`auth/oauth.go:85` joins the flow-cookie payload with `|`; `oauth.go:120-123` requires
exactly 4 parts after `strings.Split`. `sanitizeNext` (`:76-81`) only checks the leading
slash. `device/pages.go:42` now feeds `r.URL.RequestURI()` into `next`, so a crafted link
`https://ao.agentlab.in/device?user_code=A|B` sends a signed-out operator into a sign-in
that always fails at the callback with no explanation. Denial only. Fix: reorder the
payload to put `Next` last and use `strings.SplitN(payload, "|", 4)`.

**L-E. The attempt limiter is per-account, in-memory, and never evicts. CONFIRMED.**
`device/codes.go:159-190`. It resets on every restart, and it is bypassed linearly by using
N Google accounts (10N guesses per minute), which the `ponytail:` comment at `:154-158`
does not mention alongside the replication caveat it does mention. `l.seen` also keeps one
entry per account that has ever submitted a code, with no eviction. Both are small today;
worth a line in the comment so the next reader does not over-trust the control.

**L-F. `createDeviceCode` retries on any insert error, not only a `user_code` collision.
CONFIRMED.** `device/store.go:55-70` treats every error as a collision and redraws up to 5
times. A `SQLITE_BUSY` or a foreign-key failure costs five wasted inserts and then reports
only the last error. Fix: retry only when the error names a `user_code` uniqueness
violation.

**L-G. `parseClaudeAuthStatus` embeds the harness's raw output into a check message that
crosses the wire. SUSPECTED (path not reachable with today's harness).**
`doctor.go:269` builds `no JSON object in output %q` from the full ANSI-stripped
`CombinedOutput`, and `doctor.go:233` puts that into the check `Message`. It only fires when
`claude auth status --json` exits **zero** and prints no `{`, because `cmdErr` takes
precedence when non-zero (`doctor.go:229-232`). I could not find a `claude` version that
prints a credential on that path, so I am flagging the shape, not a live leak. Fix:
`FirstOutputLine(out)` and a length cap instead of the whole buffer.

**L-H. Previous review's L6 is not closed.** `vmgateway/config.go:199-205` still resolves
the default `machine.json` from `os.UserHomeDir()` rather than the `AO_DATA_DIR`-honouring
data dir, while `CertDir` right above it does honour it (`config.go:137-142`). So
`AO_DATA_DIR=/srv/ao` moves the certificates but not `machine.json`.

## 3. Did the previous review's fixes close

- **H1** duplicate CORS: **closed.** `proxy.go:91` wires `ModifyResponse = dropUpstreamCORS`,
  which deletes all six headers (`proxy.go:31-38`, `:108-113`). It covers the
  websocket-upgrade path: `httputil.ReverseProxy` calls `modifyResponse` before
  `handleUpgradeResponse` on a 101. The proxy-error path never touches upstream headers at
  all, because `ErrorHandler` (`proxy.go:92-96`) writes on the gateway's own
  `ResponseWriter`, on which `corsGate` already used `Set`, not `Add`. The fake daemon in
  `proxy_test.go` now emits CORS headers, so the fix is tested.
- **H2** `publicUrl` normalization: **closed.** `normalizeDomain` (`config.go:184-197`)
  parses off a scheme and rejects anything still containing `:` or `/`. Against the four
  cases asked about: **port** is silently dropped (see L-B), **path** is dropped by
  `u.Hostname()`, **userinfo** is dropped by `u.Hostname()`, **IDN** works correctly
  because `autocert.HostWhitelist` and `autocert.Manager.GetCertificate` both run
  `idna.Lookup.ToASCII`, which also case-folds, so `VM.Example.COM` and `ünïcode.example.com`
  both match their SNI form.
- **H3** SSE cookie fallback: **closed.** `cookieAuthAllowed` (`proxy.go:242-247`) is
  `path == "/mux"` or (`GET` and `path == "/api/v1/events"`), compared with `==` against
  `r.URL.Path`, which Go has already percent-decoded and which excludes the query string.
  A trailing slash (`/api/v1/events/`) does not match, a query string cannot widen it, and
  no state-changing method on any other path accepts the cookie. `/mux` accepts the cookie
  on any method, but a cross-origin POST there carries an `Origin` and is 403'd by
  `corsGate` first; the no-Origin residue is L-A.
- **H4** JWKS backoff: **closed.** `Get` (`jwks.go:127-154`) releases `c.mu` before
  `c.fetch` and serves a concurrent arrival the stale keyset via the `c.refreshing` flag
  rather than queueing it on the mutex. The failure path sets
  `c.fetchedAt = now + failureBackoff - ttl` (`:146`), which is state on the cache, not on
  the request, so the backoff is genuinely per-cache. Cold start still lets concurrent
  callers each fetch; that is documented at `:125-126` and is the right trade.
- **M1** schema comment: **closed.** `0001_init.sql:14-19` now says the audience is
  `machines.id`, not `hostname`.
- **M3** `DATA_DIR` required: **closed.** `config.go:101-104` fails at boot with a message
  naming `.env.example`, `filepath.Abs` resolves it (`:105-109`), and
  `cmd/controlplane/main.go:52-54` logs the resolved absolute dir, the active `kid`, the
  TTL, and the public origin on every boot. Both halves of the ask landed.
- **M4** `PUBLIC_ORIGIN` normalization: **closed.** `config.go:119` trims once;
  `auth/auth.go:60-62` now relies on it instead of re-trimming.
- **M5** no cross-module test: **closed.** `backend/internal/vmgateway/testdata/` holds a
  `jwks.json` from the real `keys.Manager.JWKS()` and two tokens from the real
  `Issuer.IssueAccessToken`, generated by `controlplane/internal/tokens/golden_test.go` and
  consumed by `backend/internal/vmgateway/golden_test.go`, with the mirror assertion in
  `controlplane/internal/keys/golden_test.go`. One gap left: no control-plane-audience token
  is in the fixture set, so "the gateway rejects a control-plane token" is covered only by
  the generic `aud` unit test, not end to end.
- **L1** `keys.Rotate` write order: **closed.** `keys.go:161-180` writes `next.json` first,
  `active.json` second, memory last, with the reasoning in the doc comment.
- **L2** forwarded headers: **closed.** `stripForwardedFor` (`proxy.go:120-123`) deletes
  `X-Real-IP` and `X-Forwarded-For` in the director, and `r.Host` is deliberately left
  alone, which is what keeps `localControlRequest` correctly refusing.
- **L3** preflight before allowlist: **closed.** `proxy.go:296-299` calls `isProxyablePath`
  inside the preflight branch and 404s.
- **L4** `/mux` header fallback: **closed.** `extractToken` (`proxy.go:216-232`) tries the
  cookie on the allowed routes and falls through to `Authorization` everywhere.
- **L5** `ACCESS_TOKEN_TTL` bounds: **closed.** `config.go:33-34` and `:126-129` reject
  outside `[10m, 30m]` at load with an explanatory error. The control-plane-audience token
  respects the same bound: `IssueControlPlaneToken` uses the same `i.accessTTL`
  (`controlplane.go:62`) as `IssueAccessToken` (`access.go:72`), and both endpoints report
  `AccessTokenTTL()` as `expires_in`.
- **L6** `machine.json` vs `AO_DATA_DIR`: **not closed.** See L-H.
- **L7** data dir 0700: **closed.** `storage/sqlite/db.go:43` and `auth/session.go:49` are
  both `0o700`, with a test (`TestOpen_CreatesDataDirMode0700`).
- **L8** untested rejection paths: **partially closed.** All three gateway gaps it named now
  have tests (upstream CORS not duplicated, a failing JWKS fetch not refetched, `Resolve`
  rejecting a URL where a hostname is required). The clone-flow gaps are untouched and are
  outside this batch.
- **M2** `cloneUrl` credential: not in my half (#20).

### Contract drift against the previous review's section 3 table

Every row of that table still matches. Of the six follow-ups it listed:

1. `0001_init.sql` audience comment: closed.
2. `PUBLIC_ORIGIN` normalization: closed.
3. SSE Bearer transport: closed. `TOKEN_CONTRACT.md:70-84` now names the cookie for
   `GET /api/v1/events` and states the cookie is never accepted on a state-changing method.
4. `publicUrl` as origin vs hostname: resolved in code on both sides
   (`device/codes.go:109-131` emits an origin, `vmgateway/config.go:184-197` reduces it) but
   `TOKEN_CONTRACT.md` still does not state it. The reasoning lives only in two Go doc
   comments. Worth one line in the contract before the next `setup-vm` change.
5. Cookie name and `SameSite`: closed. `TOKEN_CONTRACT.md:86-101` names `ao_gw_token` and
   mandates `SameSite=None; Secure` with the reasoning.
6. Refresh TTL and rotate-on-use: closed. `TOKEN_CONTRACT.md:19-27`.

New this batch, and consistent: the two-audience split is documented at
`TOKEN_CONTRACT.md:29-51` and matches the implementation exactly, including "neither side
accepts a list, a wildcard, or a missing `aud`" (`controlplane.go:104`, `token.go:147`).

### Migrations

`0002_device_code_machine.sql` is three additive `ALTER TABLE ... ADD COLUMN` on
`device_codes`, so it composes with `0001` cleanly: the table exists, and `machines` exists
for the `machine_id TEXT REFERENCES machines (id)` foreign key, which is genuinely enforced
(`_pragma=foreign_keys(ON)`, `db.go:29`). `approve` inserts the machine row before it sets
`machine_id`, inside the same transaction (`store.go:152-164`), so the ordering is right.

It is **not** idempotent on its own (a second `ADD COLUMN machine_name` is a duplicate
column error), but goose's version table means it never runs twice, and the whole
`controlplane` suite plus every device test exercises the resulting schema. The `Down`
migration needs SQLite 3.35+ for `DROP COLUMN`, which modernc v1.55 provides; nothing tests
the down path (`migrate_test.go` only asserts the four tables exist and the dir mode).

## 4. What I verified and found correct

Does not need re-reviewing.

**The device flow's security properties, read line by line:**

- **`user_code` entropy and alphabet are right.** `codes.go:47-59` draws each character with
  `crypto/rand.Int` against `big.NewInt(20)`, not a byte modulo 20, so there is no bias
  toward the first 16 letters. The alphabet `BCDFGHJKLMNPQRSTVWXZ` (`:22`) is RFC 8628's
  suggested set: no vowels (a code cannot spell a word), no digits, and none of `0/O`,
  `1/I/L`, `5/S`, `8/B`. 8 characters is ~34.6 bits. `normalizeUserCode` (`:93-101`) drops
  out-of-alphabet characters rather than rejecting, so a typed `0` and `O` both normalize to
  a lookup that simply misses rather than to a distinguishable error.
- **`device_code` is 32 bytes of `crypto/rand`, base64url** (`codes.go:63-69`), so 256 bits.
  It genuinely needs no rate limit of its own.
- **It is stored hashed, and the schema comment is accurate.** `hashCode` is SHA-256 hex
  (`codes.go:75-78`); the insert writes `hashCode(deviceCode)` (`store.go:64`) and the poll
  looks up `WHERE device_code = hashCode(deviceCode)` (`store.go:264`). The plaintext is
  never written. Plain SHA-256 is the correct choice here, not bcrypt: the input is 256 bits
  of CSPRNG output, not a password. Timing: the comparison is an indexed B-tree lookup on a
  fixed-width digest, and since an attacker cannot choose the digest without a preimage,
  there is nothing for a timing side channel to reveal.
- **Polling is rate limited server side, not by trusting `slow_down`.** `poll`
  (`store.go:246-325`) reads `last_polled_at` and returns `slow_down` when
  `now.Sub(last) < pollInterval` (`:295-297`), inside the same transaction that advances
  `last_polled_at` (`:299-301`), so two concurrent polls cannot both pass the gate. It
  deliberately does not advance `last_polled_at` on a `slow_down`, so a misbehaving client
  is throttled to one poll per interval rather than locked out permanently. Tested at
  `flow_test.go:382`.
- **Expiry is enforced server side on every read path**, not merely displayed: `poll`
  (`store.go:277-289`), `lookupPending` (`:99-102`), and `approve` inside its transaction
  (`:148-150`). `markExpired` is bookkeeping only and its failure cannot make an expired
  code usable, which the comment states and the code backs up. Tested at `flow_test.go:413`
  and `:438`, including the "post the decision directly, skipping the page" case.
- **A code cannot be approved twice, including by a racing second account.** `approve`
  re-reads the row inside the transaction, checks `status != pending`, and then guards the
  UPDATE with `AND status = ?` plus a `RowsAffected() == 0` check (`store.go:160-170`). A
  second approval loses cleanly with 409. Tested at `flow_test.go:463`, which additionally
  asserts only one `machines` row exists and the binding stays with the first account.
  My empirical SQLite check (M-B) confirms the single-winner property holds under real
  concurrency, not just in the sequential test.
- **The approval POST validates the session, not just the GET.** `handleDecision` calls
  `requireAccount` first (`pages.go:117`), which goes through
  `sessions.AccountFromRequest` → the HMAC-SHA256-signed, expiry-carrying `ao_session`
  cookie (`auth/session.go:116-137`). A signed-out POST is redirected, not honoured, and the
  code stays pending. Tested at `flow_test.go:506`.
- **Following `verification_uri_complete` cannot approve anything.** The GET only renders a
  prefilled form (`pages.go:48-55`); the confirm page is a separate POST and the approval a
  third. Tested at `flow_test.go:601`, which asserts no machine row after reaching the page.
- **Re-binding the same VM keeps its machine id.** `registerMachine` (`store.go:201-226`)
  reuses the unrevoked row for `(account_id, hostname)` rather than always inserting, so a
  re-run of `ao setup-vm` does not orphan previously minted tokens. Tested at
  `flow_test.go:619`.
- **`normalizePublicURL` is strict about everything except the scheme** (M-E):
  `codes.go:109-131` rejects userinfo, query, fragment, a path, and a non-http(s) scheme,
  and lowercases the host. Tested at `codes_test.go:80-115`.

**The two-audience split, and the tests that claim to cover it:**

- `IssueControlPlaneToken` sets `Aud: i.issuer` (`controlplane.go:61`), the same string as
  `iss`, which is `cfg.PublicOrigin` (`main.go:67`). `VerifyControlPlaneToken` requires
  **both** `claims.Iss == i.issuer` **and** `claims.Aud == i.issuer` (`:104`), so the
  audience is pinned to this deployment's origin and a token minted by a staging control
  plane cannot be replayed against production even if the two shared a signing key.
- `VerifyControlPlaneToken` checks the signature strictly before it unmarshals any claim
  (`:96-101`), pins `alg` to `EdDSA` before consulting a key (`:87-91`), rejects a missing
  `exp` as a rejection rather than "no expiry" (`:107`), and rejects an empty `sub`
  (`:110`). `splitJWT` (`:118-137`) rejects a fourth segment.
- **The tests assert what they claim.** I read them rather than trusting the names.
  `TestListMachines_RequiresAControlPlaneAudienceToken` (`machines_test.go:78-140`) sends a
  real machine-audience token, a real refresh token, a token from a *different* control
  plane (signed by another key), a tampered signature, an empty bearer, and a
  session-cookie-only request, requires 401 with a `Bearer` challenge on every one, and then
  requires 200 for the correct credential so none of it passes for the wrong reason.
  `TestAuthenticate_AcceptsOnlyAControlPlaneAudienceBearerToken` (`api_test.go:160-206`)
  covers the same set at the middleware level plus scheme handling.
- **The refresh token is accepted only at the token endpoint.** It is opaque base64url and
  fails `splitJWT` on the resource path; the only other `/api/v1` route is
  `GET /api/v1/machines` (`device.go:111`), which goes through the same
  `apiSvc.Authenticate` (`main.go:72`). Asserted explicitly in `machines_test.go:94-97,109`.
- **Rotation does not leak which failure occurred.** `handleToken` (`api.go:79-87`) collapses
  `ErrInvalidRefreshToken`, `ErrRefreshTokenRevoked`, and `ErrRefreshTokenExpired` into one
  `invalid_grant` with one description. `TestTokenEndpoint_RejectsBadRequestsIdentically`
  (`api_test.go:115-157`) asserts the revoked and the unknown case produce the same code and
  that the description does not say "revoke". All three failures cost one indexed SELECT,
  so they are not timing-separable either.
- **Rotation itself is single-use under concurrency**, see M-B: exactly one exchange commits
  in every run, and the loser's only problem is the error shape it gets back.

**Other things read and found correct:**

- `html/template` contextual escaping is in force on all four device templates
  (`device.go:81` uses `template.ParseFS` on `html/template`), and the two
  attacker-controlled values (`machine_name`, `machine_public_url`) appear only in element
  text, never in an attribute or a URL context. No XSS vector.
- No token, key, or code material reaches a log. `endpoints.go:85,134,147` log wrapped
  errors, never the device code or the token; `pages.go:131,148,155` log the *user* code on
  a failure path, which is short-lived and already known to the operator, and never the
  device code. `keys.go` never formats a private key.
- `envelope`/`api.WriteJSON` sets `Cache-Control: no-store` on every control-plane response
  (`api.go:177`), which matters because these bodies carry bearer secrets.
- The gateway's token verifier (`vmgateway/token.go`) is unchanged from the previous review
  and still correct: signature before claims, `alg` pinned, signature length checked,
  `iss`/`aud`/`sub`/`exp` all checked, `VerifyOptions` refusing to run with any of the three
  empty, fail-closed on an empty key set.
- The gateway's deny-by-default path allowlist is unchanged and still stronger than the LAN
  listener's blocklist. `GET /api/v1/doctor` is inside the proxyable set by design.
- `ao vm serve` still matches ADR 0002: separate process, `autocert.HostPolicy` scoped to the
  one configured domain (`server.go:46`), `:80` never serving plaintext
  (`manager.HTTPHandler(nil)`, `server.go:55`), `ReadHeaderTimeout` on both listeners and no
  write timeout (correct for SSE and `/mux`), graceful shutdown draining both goroutines.
- `internal/doctor` preserves the CLI contract the desktop depends on: `ClaudeHarnessName`
  and `ClaudeAuthStatusArgs` are exported constants read by both `ao vm setup-harness`
  (`cli/vm.go:110-114`) and the `claude-auth` check, the check `Name` is the literal
  `"claude-auth"` (`doctor.go:207`), and `Remediation` is populated on every WARN branch of
  that check (`doctor.go:209-214`). The `Check` JSON tags are unchanged, so
  `ao doctor --json` output is byte-identical; the HTTP DTO is a separate projection
  (`controllers/dto.go:793-799`).
- Shelled-out probes that could echo a credential: `githubToken` never puts the token in a
  message (`doctor.go:574-595`); the `gh auth token` failure path wraps an `*exec.ExitError`,
  whose `Error()` is `"exit status N"` and not the output, so the token cannot ride out that
  way. The one raw-output path is L-G.

**Anything batch 5's remote transport will find inadequate:** two things, both already
above. The `SameSite=None` cookie the contract mandates plus `corsGate`'s pass-through on a
missing `Origin` (L-A) is the one place the transport story is thinner than it reads. And
`GET /api/v1/doctor` will be called by the desktop machine card on a timer, at which point
M-C's per-request six subprocesses and GitHub API call stop being theoretical.

**Pre-existing and not caused by this work:** `go test ./internal/cli/` still has the 5
failures the previous review recorded. None of the five commits in this batch touch
`internal/cli/spawn.go` or the agents route.
