# Desktop login contract

The two control-plane endpoints the desktop app's sign-in talks to. Written by the
desktop side (issue #25) while the control plane's own routes were being built in
parallel (issue #23), so the app builds against this contract rather than waiting.
If the control plane lands different paths or field names, this file and
`frontend/src/main/ao-pkce.ts` change together.

Token shapes, lifetimes, and the rotate-on-use rule live in
[`controlplane/TOKEN_CONTRACT.md`](../controlplane/TOKEN_CONTRACT.md); this file is
only the desktop login exchange.

## Why this shape

The desktop app is a **public client**: it ships no secret. Google blocks OAuth in
embedded webviews, so login runs in the user's real browser and comes back through
an RFC 8252 loopback redirect. PKCE (S256) plus a CSPRNG `state` is the whole
defence against another local process stealing the authorization code.

`AO_CONTROL_URL` selects which control plane the app trusts (default
`https://ao.agentlab.in`; plain HTTP is accepted only on the loopback, for a
locally-run control plane). It can never skip authentication, and there is no flag
that does.

## `GET /oauth/desktop/authorize`

Opened in the system browser. Query parameters:

| Parameter | Value |
| --- | --- |
| `response_type` | `code` |
| `client_id` | `ao-desktop` |
| `redirect_uri` | `http://127.0.0.1:<ephemeral>/callback` |
| `code_challenge` | base64url(SHA-256(verifier)) |
| `code_challenge_method` | `S256` |
| `state` | 32 CSPRNG bytes, base64url |

The control plane signs the user in with Google (its existing `/auth/google/login`
session), then redirects to `redirect_uri` with `code` and the unmodified `state`,
or with `error` plus optional `error_description`.

The redirect URI is a loopback address on an ephemeral port, per RFC 8252 section
7.3, so the control plane must accept any port for `127.0.0.1` on this client. The
desktop listener accepts exactly one callback, validates `state` before reading the
code, and shuts down immediately.

## `POST /oauth/desktop/token`

`Content-Type: application/x-www-form-urlencoded`:

```
grant_type=authorization_code
client_id=ao-desktop
code=<from the callback>
code_verifier=<the PKCE verifier>
redirect_uri=<the same loopback URI>
```

Success, `200`, JSON:

```json
{
  "refresh_token": "<opaque, high entropy>",
  "account": { "id": "<accounts.id>", "email": "<google email>" }
}
```

Failure returns a non-2xx with the OAuth error envelope `{"error": "...",
"error_description": "..."}`; `invalid_grant` covers a reused code, a mismatched
verifier, and a mismatched `redirect_uri`.

**No access token is issued here.** Login yields identity plus the refresh token and
nothing else; every access token comes from a later exchange at the token endpoint.

## Two token audiences, deliberately not interchangeable

Decided by the orchestrator after `controlplane/TOKEN_CONTRACT.md` merged, because
that file only covered machine-audience tokens and said nothing about how a desktop
install authenticates to the control plane itself, which it must do before it has
picked any machine. `hosted-ao-17` is implementing the token endpoint and adding a
section to `TOKEN_CONTRACT.md` defining both audiences; that file wins once it does.

- **Control-plane audience.** `aud` is the control plane origin
  (`https://ao.agentlab.in`). This is what authenticates control-plane API calls
  such as `GET /api/v1/machines`. Obtained by exchanging the refresh token at the
  token endpoint.
- **Machine audience.** `aud` is a `machines.id`, verified by that machine's
  gateway. Obtained per machine, and only after one is chosen. Task 13 in batch 5.

The refresh token goes **only** to the control plane's token endpoint. It is never a
Bearer credential on a resource route, never in a URL, and never in a log line. An
access token of one audience is never sent to the other.

Neither exchange is implemented here: this task ends at "a refresh token is stored".
When task 13 adds a cached control-plane access token, **sign-out must discard that
cache as well as the stored refresh token.** Today there is nothing cached to
discard, so deleting the file is the whole of sign-out.

## What the desktop stores

`~/.ao/ao-account.json`, mode 0600 (the `~/.ao` state dir, `AO_RUN_FILE`-relative,
never an OS default app-data location):

```json
{ "version": 1, "accountId": "...", "email": "...", "refreshToken": "<base64 ciphertext>" }
```

The refresh token is encrypted with Electron `safeStorage` (macOS Keychain,
libsecret, DPAPI). When `safeStorage.isEncryptionAvailable()` is false the app
refuses to sign in and says why, rather than writing the credential in plaintext.
Sign-out deletes this file, so the credential is actually gone from this end.

Because refresh tokens rotate on every use, the refresh exchange must persist each
replacement through `writeStoredAccount` in
`frontend/src/main/ao-account-store.ts` or the next refresh replays a revoked
token.
