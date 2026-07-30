> **Preserved from `/tmp/ao/review-client-side_report.md`.** `/tmp` is ephemeral, so this is
> the only copy. Everything below the horizontal rule is the report exactly as the reviewer
> wrote it: no summarizing, no rewording, no reordering. The value is the file, the line, the
> failure scenario, and the smallest fix, and a summary would destroy all four. Section 3,
> the ranked pre-flight risk list for the batch 5 VM run, has not happened yet and is the
> checklist for task 14.
>
> | | |
> | --- | --- |
> | Reviewed state | `develop` @ `a7eb9c6b2` |
> | Date | 2026-07-30 |
> | Reviewer session | `hosted-ao-25` (AO worker, read-only) |
> | Findings | 22: 1 critical (suspected), 4 high, 9 medium, 8 low |
>
> **Where each finding was fixed.** Every finding was fixed. This mapping was added on
> import and is not part of the original report.
>
> | Finding | Fixed in | Issue |
> | --- | --- | --- |
> | C1 `WorkingDirectory` emitted quoted, systemd refuses the unit | [#46](https://github.com/agentlab-in/hosted-ao/pull/46) | [#38](https://github.com/agentlab-in/hosted-ao/issues/38) |
> | H1 nothing stops both units installing as `User=root` | [#46](https://github.com/agentlab-in/hosted-ao/pull/46) | [#38](https://github.com/agentlab-in/hosted-ao/issues/38) |
> | H2 the outside-in reachability check can never run | [#46](https://github.com/agentlab-in/hosted-ao/pull/46) | [#38](https://github.com/agentlab-in/hosted-ao/issues/38) |
> | H3 the `claude auth status` probe can hang forever | [#43](https://github.com/agentlab-in/hosted-ao/pull/43) | [#39](https://github.com/agentlab-in/hosted-ao/issues/39) |
> | H4 two more origin-URL writers bypass #20's stripping | [#42](https://github.com/agentlab-in/hosted-ao/pull/42) | [#41](https://github.com/agentlab-in/hosted-ao/issues/41) |
> | M1 a wrong-state hit on the loopback port aborts the login | [#44](https://github.com/agentlab-in/hosted-ao/pull/44) | [#40](https://github.com/agentlab-in/hosted-ao/issues/40) |
> | M2 harness probe budget too small, echoes the whole output | [#43](https://github.com/agentlab-in/hosted-ao/pull/43) | [#39](https://github.com/agentlab-in/hosted-ao/issues/39) |
> | M3 a selected remote machine reports `ready` with no credential | [#44](https://github.com/agentlab-in/hosted-ao/pull/44) | [#40](https://github.com/agentlab-in/hosted-ao/issues/40) |
> | M4 machine switching leaves the previous machine in the cache | [#44](https://github.com/agentlab-in/hosted-ao/pull/44) | [#40](https://github.com/agentlab-in/hosted-ao/issues/40) |
> | M5 `parseMachineOrigin` accepts plain `http:` for any host | [#44](https://github.com/agentlab-in/hosted-ao/pull/44) | [#40](https://github.com/agentlab-in/hosted-ao/issues/40) |
> | M6 `machine.json` parent directory is not chowned | [#46](https://github.com/agentlab-in/hosted-ao/pull/46) | [#38](https://github.com/agentlab-in/hosted-ao/issues/38) |
> | M7 `systemctl start` success reported as "running" | [#46](https://github.com/agentlab-in/hosted-ao/pull/46) | [#38](https://github.com/agentlab-in/hosted-ao/issues/38) |
> | M8 no start-rate-limit override | [#46](https://github.com/agentlab-in/hosted-ao/pull/46) | [#38](https://github.com/agentlab-in/hosted-ao/issues/38) |
> | M9 a failed refresh-token persist locks the install out | [#44](https://github.com/agentlab-in/hosted-ao/pull/44) | [#40](https://github.com/agentlab-in/hosted-ao/issues/40) |
> | L1 loopback page interpolates `error_description` unescaped | [#44](https://github.com/agentlab-in/hosted-ao/pull/44) | [#40](https://github.com/agentlab-in/hosted-ao/issues/40) |
> | L2 `state` comparison is not constant-time | [#44](https://github.com/agentlab-in/hosted-ao/pull/44) | [#40](https://github.com/agentlab-in/hosted-ao/issues/40) |
> | L3 DNS match compares IP strings | [#46](https://github.com/agentlab-in/hosted-ao/pull/46) | [#38](https://github.com/agentlab-in/hosted-ao/issues/38) |
> | L4 dual-stack fallback can report a false DNS mismatch | [#46](https://github.com/agentlab-in/hosted-ao/pull/46) | [#38](https://github.com/agentlab-in/hosted-ao/issues/38) |
> | L5 loopback-only port probe can pass while the public bind fails | [#46](https://github.com/agentlab-in/hosted-ao/pull/46) | [#38](https://github.com/agentlab-in/hosted-ao/issues/38) |
> | L6 sign-out can be raced by an in-flight machine refresh | [#44](https://github.com/agentlab-in/hosted-ao/pull/44) | [#40](https://github.com/agentlab-in/hosted-ao/issues/40) |
> | L7 Ctrl-C during the device flow waits out the poll interval | [#46](https://github.com/agentlab-in/hosted-ao/pull/46) | [#38](https://github.com/agentlab-in/hosted-ao/issues/38) |
> | L8 temp-file name collides on pid reuse | [#44](https://github.com/agentlab-in/hosted-ao/pull/44) | [#40](https://github.com/agentlab-in/hosted-ao/issues/40) |
>
> The "Observation, not a finding" at the end of section 2 (the device flow mints a
> machine-audience token that `ao setup-vm` deliberately never decodes, so a 15 minute
> credential is minted on every bind for no consumer) was carried into
> [#47](https://github.com/agentlab-in/hosted-ao/issues/47), which is in flight.

---

# Client-side review: CLI, `ao setup-vm`, desktop auth and machines

Reviewed state: `develop` @ `a7eb9c6b2`. Scope: #20, #27, #28, #32, #33, #35.
Read-only review. Nothing was changed, no PR, no issue. `ao setup-vm` was not run.

## 1. Verdict

Not on a VM I cared about, not yet. One suspected-critical defect in the systemd unit
text would make the very first real run fail after it has already installed packages and
written units, and there is no guard stopping both units from being installed as `User=root`
on a VM whose login user is root, which is the default on DigitalOcean and Hetzner.
The binding half, the atomic writes, the device flow, and the desktop spawn guard are good;
the outside-in port check the spec requires can never actually run.

Counts: 1 critical (suspected), 4 high, 9 medium, 8 low.

## 2. Findings

Severity is impact on the batch 5 run and on account security. Every finding is marked
CONFIRMED (read the code that proves it) or SUSPECTED (reasoned, not executed).

---

### CRITICAL

#### C1. `WorkingDirectory` is emitted quoted, which systemd rejects as a fatal setting
`backend/internal/cli/setupvm_plan.go:742`

```go
fmt.Fprintf(&b, "WorkingDirectory=%q\n", u.WorkingDir)
```

produces `WorkingDirectory="/home/ubuntu/.ao/data"`.

Status: SUSPECTED, high confidence. I could not execute it: there is no systemd on this
machine and the Docker daemon is not running, and no CI job in `.github/workflows/`
parses the generated units.

What is wrong: unit-file quoting is only honoured by settings parsed as a list of words.
`Environment=` is one of those and the quoting there is correct and documented.
`WorkingDirectory=` is not: systemd's `config_parse_working_directory` runs specifier
expansion and then `path_simplify_and_warn(..., PATH_CHECK_ABSOLUTE|PATH_CHECK_FATAL)`
with no unquoting, so the value it sees is `"/home/ubuntu/.ao/data"` starting with a
double quote, which is not an absolute path. Without a leading `-` on the value the check
is fatal, the parse handler returns `-ENOEXEC`, and the unit refuses to load rather than
merely ignoring the setting.

Failure scenario: first real run on the fresh VM. Preflight passes. `install -d` runs,
apt installs tmux/git/gh, the `ao` binary is installed to `/usr/local/bin/ao`, both unit
files are written, `systemctl daemon-reload` succeeds. Then
`systemctl enable ao-daemon.service ao-gateway.service` or
`systemctl start ao-daemon.service` fails with `Unit configuration has a fatal error`
or `Refusing`. `installSetupVM` returns that error at `setupvm.go:443` or `:453`,
`runSetupVM` returns before the binding step, and the operator is left with a
half-provisioned box, no summary, and no binding. Every re-run fails the same way.

Smallest fix: `fmt.Fprintf(&b, "WorkingDirectory=%s\n", u.WorkingDir)` and update the two
golden assertions at `setupvm_plan_test.go:525` and `:554` plus the
`strings.HasPrefix(value, "\"/")` check at `:593`. If a home directory with a space is a
concern, escape the space as `\x20` rather than quoting. Leave `Environment=%q` alone,
that one is correct.

Ten-second verification before the run, on any Linux box:

```
ao setup-vm --domain vm.example.com --dry-run    # prints both units
# paste each into /tmp/x.service, then:
systemd-analyze verify /tmp/x.service
```

---

### HIGH

#### H1. Nothing stops both units from being installed as `User=root`
`backend/internal/cli/setupvm.go:382-395`, `setupvm_plan.go:618-642`, `:703-716`

CONFIRMED.

`setupTargetUser()` falls back to `user.Current()` whenever `SUDO_USER` is unset or is
`root`. `buildSetupPlan` accepts whatever username it is handed; the only rejection is an
empty user or a non-absolute home. `systemdUnit`'s own doc comment at
`setupvm_plan.go:705-707` states the invariant "User and Group are never root", and
nothing enforces it.

Failure scenario: the operator SSHes into the VM as `root` (the default on DigitalOcean
droplets and Hetzner servers, and the result of `sudo -i` or `sudo su -` anywhere).
`SUDO_USER` is unset, `user.Current()` is root. Preflight's privilege check passes
silently because `UID == 0`. The plan resolves `User=root`, `Group=root`,
`AODir=/root/.ao`. Both units are written with `User=root`, so `ao daemon` runs every
agent session, every `git`, and every `gh` as root, and `ao vm serve` runs as root while
being handed `AmbientCapabilities=CAP_NET_BIND_SERVICE` it does not need. Nothing warns.
This is the single most likely way the batch 5 box ends up wrong in a way nobody notices.

Smallest fix: in `buildSetupPlan`, return an error when
`strings.TrimSpace(in.User) == "root"`, with remediation naming the fix
(`adduser --disabled-password ao && sudo -u ao ao setup-vm --domain ...`, or re-run under
`sudo -u <human>`). Keep it in the pure function so it is unit-testable, and add the same
check to `checkSetupPrivilege` so it surfaces as a preflight problem rather than a
post-preflight error, which preserves the "nothing was changed" guarantee.

#### H2. The outside-in reachability check the spec requires can never run
`backend/internal/cli/setupvm.go:46`, `:288-292`

CONFIRMED.

`defaultSetupVMProbeURL = vmgateway.DefaultIssuer + "/api/v1/reachability"`. A repo-wide
grep for `reachability` outside `backend/internal/cli` and the frontend finds no handler:
the control plane does not implement that route. Separately,
`probeSetupReachability` returns an empty `setupReachability{}` whenever the gateway is
not already active, which is always true on a first run.

So: first run, gateway not active, check skipped, warning printed. Second run, gateway
active, the probe gets a 404, `setupHTTPGet` returns an error at `setupvm.go:329`,
`Reach.Err` is set, warning printed. `blockedSetupPorts` returns nil in both cases
because `Ran` is never true, so the "public reachability" problem at
`setupvm_plan.go:251` is unreachable code. The spec at
`docs/superpowers/specs/2026-07-29-hosted-ao-v1-accounts-and-machines.md:130-134` lists
"confirm 80 and 443 are reachable from outside" as a preflight requirement and names the
cloud firewall as "the one thing the script cannot fix, so it must detect and instruct".
It instructs; it never detects.

Failure scenario for batch 5: the cloud security group blocks 443. Preflight passes with
a warning. Install succeeds, binding succeeds, the gateway starts, and then ACME never
gets a certificate. The symptom the operator sees is a machine that shows Offline in the
desktop for no stated reason, with the real cause buried in `journalctl -u ao-gateway`.

Smallest fix, client side: this is safe-by-design (unverified never reports as closed), so
the smallest honest change is to stop implying the check exists. Make the closing summary
carry the off-box `nc -vz <domain> 80` / `443` pair as a numbered "still missing" step
rather than only as remediation text inside a warning, so the operator is told to do the
one check the tool cannot. The server-side endpoint is the other reviewer's half; flag it
to them as unimplemented but referenced.

#### H3. The `claude auth status` probe can hang forever despite its 2 second context
`backend/internal/doctor/doctor.go:220-223` and `:129-131`

CONFIRMED (the missing guard). SUSPECTED (that `claude` triggers it in practice).

```go
reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)   // probeTimeout = 2s
out, cmdErr := d.CommandOutput(reqCtx, path, ClaudeAuthStatusArgs...)
...
func commandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
    return aoprocess.CommandContext(ctx, name, args...).CombinedOutput()
}
```

`aoprocess.CommandContext` (`backend/internal/process/command.go:17-21`) sets no
`WaitDelay` and no process group. `CombinedOutput` gives `exec` a non-`*os.File` writer,
so `exec` creates an `os.Pipe` and a copy goroutine, and `Wait` does not return until
every holder of the write end has closed it. On context expiry `exec` kills only the
direct child. `claude` is a Node CLI that spawns children; any grandchild that inherits
the pipe and survives blocks `Wait` indefinitely. This is precisely the hang class #20
had to fix in `project/clone.go` with `cmd.WaitDelay`, and the fix was not applied here.

Failure scenario: `ao doctor` on the VM never returns. Worse, #36 exposed the same
`doctor.Run` at `GET /api/v1/doctor` (`backend/internal/httpd/api.go:81-84,113`), so one
request leaks a goroutine and a hung request that never completes.

Smallest fix: in `backend/internal/doctor/doctor.go:129-131` and
`backend/internal/cli/root.go:106-108`, build the command and set
`cmd.WaitDelay = 2 * time.Second` before `CombinedOutput()`. One line each.

#### H4. #20's credential stripping is bypassed by two other origin-URL writers
`backend/internal/legacyimport/importer.go:181-191`,
`backend/internal/observe/scm/observer.go:1452-1458` and `:496-503`

CONFIRMED. `go test ./internal/service/project/` passes (147 tests); the defect is
outside that package.

`service.go:686-688` asserts "This is the single choke point for every RepoOriginURL
assignment". It is not. Two independently written resolvers with the same shape persist
the raw URL:

```go
// legacyimport/importer.go:181
func defaultRepoOriginURL(path string) string { ... return strings.TrimSpace(string(out)) }

// observe/scm/observer.go:1452
func resolveGitOriginURL(path string) string { ... return strings.TrimSpace(string(out)) }
```

Neither calls `sanitizeOriginURL`. The scm one is a background backfill at
`observer.go:496-503` that writes through `store.UpsertProject`, so it fires on an
ordinary observer poll with no user action. `trackerintake/observer.go:175` then logs the
column verbatim:

```go
o.logger.Warn("tracker intake: skipping project without tracker scope",
    "project", project.ID, "provider", cfg.Provider, "origin", project.RepoOriginURL)
```

Failure scenario: a project is added by path before it has an origin remote, then the
user adds `https://x-access-token:ghp_...@github.com/o/r.git` as origin. The next scm
observer poll persists the token into `projects.repo_origin_url`, from where it is served
by `GET /api/v1/projects/{id}` (`service.go:772`) and written to journald by the tracker
intake warning. Same for any legacy project imported by `ao import`.

Smallest fix: export `sanitizeOriginURL` from `internal/service/project` (or move it to a
tiny shared package) and wrap the return of both functions with it. Then correct the
now-false comment at `service.go:686-688`.

---

### MEDIUM

#### M1. Any wrong-state hit on the loopback port aborts the in-flight login
`frontend/src/main/loopback-callback.ts:66-70`

CONFIRMED.

```go
const result = parseCallback(url, expectedState);
if ("error" in result) {
    respond(res, 400, "Sign-in was rejected", result.error);
    finish({ error: new Error(result.error) });   // <- kills the flow
    return;
}
```

`finish` sets `done`, clears the timer, rejects the `code` promise, and closes the
listener. So the first request to `/callback` that carries a wrong or missing `state`
ends the login, permanently, before the real browser callback arrives.

Answer to "can a local web page hit the callback port and inject a code": it can reach
the port (a page can `fetch("http://127.0.0.1:<port>/callback?...")`; the response is
opaque to it under CORS but the handler still runs), and it cannot inject a code because
`state` is 32 CSPRNG bytes it does not have. What it can do, with no knowledge of
anything, is abort every sign-in attempt by spraying `/callback` across the ephemeral
port range. A browser prefetch, a security scanner, or a second stale browser tab does
the same by accident.

Smallest fix: on a `state` mismatch specifically, respond 400 and return without calling
`finish`, so the listener keeps waiting for the real callback until its timeout. Keep
`finish` for a genuine OAuth `error` param that did match `state`.

#### M2. The harness readiness probe has a 2 second budget and echoes the harness's whole output
`backend/internal/doctor/doctor.go:84`, `:220`, `:269`

CONFIRMED.

`probeTimeout = 2 * time.Second` is shared with the cheap local probes and is used for
`claude auth status --json`. A Node CLI cold start on a fresh 1 vCPU VM regularly exceeds
that, and this probe may touch the network. The failure path is a WARN whose message is
`could not read harness auth state (...: context deadline exceeded)`, which
`shared/ao-machines.ts:129-140` maps to `harness: "missing"` and tells the user to run
`ao vm setup-harness claude` again. So the readiness signal the desktop consumes is
likely to report a correctly-signed-in machine as not set up.

Second half: `parseClaudeAuthStatus` at `:269` embeds the entire cleaned combined output
in the error, unbounded, and that error becomes the check `Message`, which is served by
`GET /api/v1/doctor`. Whatever a future `claude` release prints on stderr goes into the
report and into any log that captures it.

Smallest fix: give this one check its own budget (a `harnessProbeTimeout = 10 * time.Second`
constant) and truncate the echoed output in `parseClaudeAuthStatus` to a couple of hundred
bytes.

#### M3. A selected remote machine reports `ready` though the app has no credential for it
`frontend/src/main/remote-daemon.ts:56`, `frontend/src/main.ts:1478-1481`

CONFIRMED.

`machineDaemonStatus` returns `{ state: "ready", baseUrl: machine.baseUrl }` for an online
machine. `applyActiveMachine` sets that as the app's daemon status, the renderer's
`applyDaemonStatus` (`renderer/lib/daemon-status.ts:8-9`) calls `setApiBaseUrl(baseUrl)`,
and every REST call and both EventSources re-point at the gateway. There is no
machine-audience token anywhere in the desktop yet (that is task 13, and
`ao-control-token.ts:22-24` says so), and `installRemoteDaemonCookie` only runs for the
`AO_REMOTE_URL`/`AO_REMOTE_TOKEN` env pairing. So selecting a machine from the picker
today produces a UI that says "Connected to ao-build-01" and 401s on every request.

Smallest fix: until task 13 lands, return a distinct non-ready status for a picker-selected
machine (for example `{ state: "error", code: "not_configured", message: "Reaching a
registered machine needs the remote transport, which is not in this build yet." }`), so the
app does not claim a connection it cannot make. This is the one thing batch 5's task 13
will most obviously find inadequate.

#### M4. Machine switching leaves the previous machine's data in the query cache
`frontend/src/renderer/lib/api-client.ts:39-45`; no `queryClient.clear()` anywhere in
`frontend/src/renderer`

CONFIRMED.

`setApiBaseUrl` notifies `baseUrlListeners`, which is enough for the long-lived
connections: `event-transport.ts:77-84` and `notifications.ts:199-206` both compare
`sourceBaseUrl` to the new base URL and close and reopen the `EventSource`. That part is
correct and in-flight SSE is torn down.

What is not handled is the react-query cache and in-flight fetches. Query keys carry no
machine or base-URL dimension (`sessionScmSummaryQueryKey()`, `aoMachinesQueryKey`, and
the rest), and nothing clears or resets the cache when the base URL changes. So after a
switch the UI renders machine A's projects and sessions under machine B's identity until
the refetch lands, and a request that was already in flight to machine A resolves
afterwards and writes machine A's response into the same cache key. The user can act on
the wrong machine's session list.

Smallest fix: in the renderer, subscribe to `subscribeApiBaseUrl` once at the app root and
call `queryClient.clear()` in the listener.

#### M5. `parseMachineOrigin` accepts plain `http:` for any host
`frontend/src/shared/ao-machines.ts:92`

CONFIRMED.

```go
if (url.protocol !== "https:" && url.protocol !== "http:") return null;
```

The type's own doc says "HTTPS origin of the machine's gateway". `readControlPlaneUrl`
in the same tree gets this right (`shared/control-plane.ts:39-42`: https, or http only for
loopback). A machine row with `public_url: "http://..."` is accepted here, becomes the
app's API base URL, and `isRemoteDaemonBaseUrl` (`shared/remote-daemon.ts:18`) then returns
false for it, so `credentials`/`withCredentials` silently change behaviour too.

Smallest fix: copy the loopback rule from `control-plane.ts` into `parseMachineOrigin`.

#### M6. `machine.json`'s parent directory is not chowned, so an `AO_MACHINE_FILE` override yields a file the gateway cannot reach
`backend/internal/cli/setupvm_bind.go:346-380`

CONFIRMED.

The chown logic itself is correct for the default path. `chownMachineFile` chowns to
`setupTargetUser()`, the same source as `plan.User`, so owner and `User=` cannot disagree;
and when the process is not root the file is already created by the right user, so
skipping the chown at `:387` is right. The default parent, `~/.ao`, was created by
`install -d -o plan.User` in `installSetupVM`, so traversal works.

The gap is the override. `writeMachineFile` does `os.MkdirAll(dir, 0o700)` as the current
process, and `plan.MachineFile` can be redirected anywhere by `AO_MACHINE_FILE`
(`setupvm_plan.go:650`). Under `sudo`, a parent created there is root-owned mode 0700 and
is never chowned. The 0600 file inside it is chowned to the human, who then cannot
traverse the directory to reach it. `ao vm serve` fails `ReadMachineFile` and refuses to
start, on a machine that just bound successfully. This is exactly the failure mode the
comment at `:382-385` is guarding against, one level up.

Smallest fix: chown each directory component `writeMachineFile` creates, or simpler, refuse
an `AO_MACHINE_FILE` that is not inside `plan.AODir` in `buildSetupPlan`, which also keeps
the `~/.ao` hard rule.

#### M7. `systemctl start`/`restart` success is reported as "running"
`backend/internal/cli/setupvm.go:456`, `:480`, `setupvm_bind.go:138-142`

CONFIRMED.

Both units are `Type=simple`, for which systemd considers the job done as soon as the
process is forked. So `systemctl start ao-daemon.service` returns 0 even when the process
exits one millisecond later, and setup-vm prints
`==> ao-daemon.service enabled and running` and
`==> ao-gateway.service restarted, so it has read the new binding` on evidence it does not
have. On the first real run, where the units have never executed, this is the difference
between a clear failure and a green summary over a crash loop.

Smallest fix: after each start/restart, run `systemctl is-active --quiet <unit>` (the helper
already exists at `setupvm.go:240-246`) and turn a negative into a note carrying
`systemctl status <unit>` and `journalctl -u <unit> -n 50`.

#### M8. No start-rate-limit override, so a crash-looping gateway gives up permanently
`backend/internal/cli/setupvm_plan.go:752-753`

CONFIRMED (the omission). SUSPECTED (that it bites in batch 5).

The units set `Restart=on-failure` and `RestartSec=5` and nothing else. systemd's default
`StartLimitBurst=5` over `StartLimitIntervalSec=10s` still applies, and with a 5 second
`RestartSec` a unit that fails immediately burns its budget and enters `failed`, where
systemd stops retrying until someone runs `systemctl reset-failed`. A gateway that cannot
bind :443 for the first 30 seconds after boot, or that exits on a transient ACME error,
stops for good and never comes back on its own. On an unattended VM nobody notices until
the desktop shows the machine Offline.

Smallest fix: add `StartLimitIntervalSec=0` (or a burst wide enough to matter, plus
`Restart=always` for the gateway, which holds no session state) to `renderSystemdUnit`.

#### M9. Refresh rotation is persisted first, but a failed persist locks the install out
`frontend/src/main/ao-control-token.ts:106-113`, `frontend/src/main/ao-account-store.ts:118-124`

The #35 claim is CONFIRMED correct: `await writeStoredAccount(...)` at
`ao-control-token.ts:109` happens before `cached` is set and before `accessToken` is
returned, and the single-flight at `:118-128` prevents two concurrent exchanges from
racing the rotation. `signOut` does clear the cache: `main.ts:1439-1445` awaits
`aoMachines().reset()`, which calls `tokenSource?.clear()` at `ao-machines.ts:331`.

The residual hole: if `writeStoredAccount` throws (safeStorage flipped unavailable, disk
full, read-only home), the exchange throws and the access token is discarded, while the
refresh token now on disk has already been revoked server-side by the rotation. Every
later exchange gets `invalid_grant`. The same applies to the crash window: there is no
`fsync` before the `rename` at `ao-account-store.ts:123`, so a power loss can lose the
replacement even after the promise resolved.

Failure scenario: the user's keychain is locked or the disk is full, the app tries one
refresh, and the sign-in is dead until they sign in again. It is a forced re-login, not a
security hole, but it is silent and the reported error will be about the write, not about
the account.

Smallest fix: `fsync` the temp file's handle before the rename, and on a write failure
after a successful exchange, surface a message that says the sign-in has to be redone
rather than the raw filesystem error.

---

### LOW

#### L1. The loopback page interpolates `error_description` into HTML unescaped
`frontend/src/main/loopback-callback.ts:27-35` and `:68`, with
`frontend/src/main/ao-pkce.ts:90-94`

CONFIRMED. `describeOauthError` returns `Sign-in failed: ${detail}` built from the
callback's `error_description` query parameter, and `PAGE()` drops it into `<title>` and
`<p>` with no escaping. Exploiting it requires knowing `state`, so the only party that can
is the authorization server itself (or a redirect it controls), and the injected script
runs in an `http://127.0.0.1:<ephemeral>` origin that holds nothing. Fix: HTML-escape
`heading` and `body` in `PAGE`.

#### L2. `state` comparison is not constant-time
`frontend/src/main/ao-pkce.ts:76`. `state !== expectedState` short-circuits on the first
differing byte. Extracting 43 base64url characters through HTTP round-trip timing on
loopback is not realistic, but the fix is one line: `crypto.timingSafeEqual` on equal-length
buffers, with a length check first.

#### L3. DNS match compares IP strings instead of `net.IP.Equal`
`backend/internal/cli/setupvm_plan.go:331-335`. `ip == pf.PublicIP` fails on any
non-canonical form. The realistic trigger is `--public-ip 2001:0DB8::1` against a
`LookupHost` answer of `2001:db8::1`, which reports a DNS mismatch that does not exist.
Fix: `net.ParseIP(ip).Equal(net.ParseIP(pf.PublicIP))`.

#### L4. Dual-stack fallback can fail preflight against a correct A record
`backend/internal/cli/setupvm.go:51`, `:254-268`. I checked the two endpoints:
`api.ipify.org` publishes A only (no AAAA), `ifconfig.me` publishes both. So the ordering
mostly saves this: the first endpoint is reached over IPv4 and returns the v4 address. But
if `api.ipify.org` is unreachable (Cloudflare blocked, DNS hiccup) and the box prefers
IPv6, `ifconfig.me` answers with the v6 address and `checkSetupDNS` reports a mismatch
against a perfectly good A record. The remediation is IPv6-aware
(`setupvm_plan.go:345-348` picks AAAA correctly), so the advice is not wrong, just
unnecessary. Fix: prefer an IPv4 answer when the resolved set is IPv4, or try both
families and accept a match on either.

#### L5. Loopback-only port probe can pass while the public bind will fail
`backend/internal/cli/setupvm.go:215-231`. A listener bound to the box's public address
only (`1.2.3.4:80`) leaves `127.0.0.1:80` free, so the probe passes and `ao vm serve`'s
`:80` wildcard bind fails later. The comment explains why a public bind is not done here,
which is right; the residual false pass is worth one line in the warning text.

#### L6. Sign-out can be raced by an in-flight machine refresh
`frontend/src/main.ts:1439-1445`, `frontend/src/main/ao-machines.ts:281-287`. A `refresh()`
already past `tokenSource.get()` (cached token in hand) can complete after `reset()` and
call `setActive(stillRegistered)`, re-writing `ao-machine.json` for a signed-out install.
Fix: a monotonic generation counter checked before `setActive`.

#### L7. Ctrl-C during the device flow waits out the poll interval
`backend/internal/cli/setupvm_bind.go:183-186`. `c.deps.Sleep(interval)` is a plain
`time.Sleep`; `ctx.Err()` is only checked afterwards, so an interrupt takes up to the
server-supplied interval (5s, up to 60s after `slow_down` backoff) to be noticed. Fix:
select on `ctx.Done()` and a timer.

#### L8. Temp-file name collides on pid reuse
`frontend/src/main/ao-account-store.ts:121-122` and `ao-machines.ts:133-134`. The temp
name is `.ao-account-${process.pid}.json`. `writeFile`'s `mode` applies only on create, so
a stale file left by a crashed run with the same pid is reused with whatever mode it had.
Fix: append a random suffix, or `rm` the temp path first.

### Observation, not a finding

The device flow mints a machine-audience access token and returns it in the
`/device/token` body, and `setupvm_bind.go:66-71` deliberately does not decode it. Not
decoding it is the right client behaviour. The consequence is that a 15 minute credential
for the machine is created and transmitted on every bind for no consumer. The issuing side
is the other reviewer's half; worth raising with them.

## 3. Pre-flight risk list for the batch 5 VM run

Ranked by probability times cost. This is what I would check before touching the box.

1. **`WorkingDirectory=` quoting (C1).** Highest probability, highest cost: it fails after
   the box has been mutated and every re-run fails identically. Verify with
   `ao setup-vm --dry-run` plus `systemd-analyze verify` on any Linux box before the real
   run. If it reproduces, this is a one-character fix.

2. **Running as root (H1).** If the VM's login user is root (DigitalOcean, Hetzner,
   or any `sudo -i`), the install silently produces root-owned units and root-run agents.
   Decide the target user before the run: create an unprivileged user, `ssh` in as them,
   and run `sudo ao setup-vm --domain ...` so `SUDO_USER` is set. Check the dry-run's
   `run as` line says the human, not `root`, before proceeding.

3. **The gateway starts but never gets a certificate, and setup-vm says it is running
   (M7 plus H2 plus M8).** `autocert` issues lazily on the first TLS handshake, so nothing
   in the run proves ACME works. Expect the first `https://<domain>/` connection to hang
   for several seconds and probably time out while the order completes, and expect the
   desktop's 4 second probe (`ao-machines.ts:37`) to report the machine Offline on the
   first refresh. Immediately after the run, by hand:
   `systemctl is-active ao-daemon ao-gateway`, `journalctl -u ao-gateway -n 100`, then
   `curl -sv https://<domain>/` twice, then confirm a file appeared under
   `~/.ao/data/vm-gateway/certs`. Also confirm 80 and 443 from your laptop with
   `nc -vz <domain> 80` and `443`, because the tool will not do it for you.

4. **Cloud firewall on 80 and 443.** Open the security group before the run, not after.
   Preflight will pass without it and everything downstream fails silently. `ufw` on the
   box is the second layer, and Ubuntu cloud images usually have it inactive; check both.

5. **DNS must already be correct and propagated.** Preflight fails closed and cleanly here,
   which is right, but each failed attempt costs a round trip. `dig +short <domain>` from
   your laptop must equal the box's public IP before the first run. Use a short TTL.

6. **`apt-get` and the GitHub CLI repo.** A fresh VM often has `unattended-upgrades`
   holding the dpkg lock in its first minutes, and `apt-get update` will fail with
   "could not get lock". That aborts the run at `setupvm.go:500` with the box only
   partly changed (directories created). Wait for cloud-init to settle
   (`cloud-init status --wait`) before running. `ensureGitHubCLIRepo` also downloads a
   keyring over the network with no fingerprint pin; that is a documented GitHub path,
   but it is another network dependency at install time.

7. **`ao doctor` may hang on the harness probe (H3).** After
   `ao vm setup-harness claude`, run `ao doctor` with a wall-clock timeout
   (`timeout 30 ao doctor`) so a hung probe is visible as a hang and not as a slow box.
   Expect the `claude-auth` check to WARN with a deadline error even on success (M2).

8. **The device flow needs a human at the terminal and a browser.** Fifteen minute code
   lifetime, two restarts allowed, and a closed stdin answers "no" to the restart prompt
   (`setupvm_bind.go:226-237`). Do not run setup-vm under `nohup`, `screen -d`, or any
   wrapper that detaches stdin. Have the browser tab and the AO account ready before
   starting the run.

9. **Clock skew.** Both ACME and the gateway's JWT verification are time-sensitive
   (`vmgateway.DefaultSkew`), and preflight does not check the clock. Confirm
   `timedatectl` shows NTP synchronized before the run.

10. **Selecting the machine in the desktop will not work yet (M3).** Expect the picker to
    say Connected and every request to 401. That is task 13, not a regression, but do not
    spend time debugging it as one.

11. **Re-running setup-vm re-binds.** Verified harmless: the control plane reuses the
    existing `machines` row for the same `(account, public_url)`
    (`controlplane/internal/device/store.go:201-225`), so the machine id is stable and the
    desktop's persisted machine does not go stale. It does need a human to approve in a
    browser again on every run, so budget for that on each retry.

## 4. What I verified and found correct

`ao setup-vm`

- **Preflight changes nothing on failure. Confirmed.** Traced every path: the platform
  gate reads `/etc/os-release` and `LookPath` only; the preflight probes are `sudo -n true`,
  a DMI read, two HTTPS GETs, a DNS lookup, `systemctl is-active`, and a bind-and-release
  on loopback. `buildSetupVMPlan` (which can also fail, on a relative `AO_DATA_DIR`) runs
  after preflight and before the first mutation. `installSetupVM` is the first thing that
  touches the box and it is only reached with `problems` empty and `--dry-run` off.
- **Idempotency. Confirmed on every axis I could check.** `writeSetupFile` compares content
  before writing, so units are never duplicated and no file grows. `ensureSetupPackages`
  skips installed packages via `dpkg-query`, and only touches the GitHub apt source when
  `gh` is actually missing. `install -d` re-applies mode and ownership rather than failing
  on an existing directory. `ensureSetupBinary` compares a SHA-256 of both files, so an
  unchanged binary is not reinstalled, and it uses `install` rather than `cp`, which
  unlinks the destination first and so avoids `ETXTBSY` against the running daemon. The
  daemon is only restarted when its unit changed, deliberately, so a re-run does not kill
  live agent sessions, and the replaced-binary case is reported as a note instead.
- **The public IP check fails closed. Confirmed.** Both endpoints unreachable, or answering
  with HTML, produce `PublicIPErr`, which `checkSetupDNS` turns into a hard preflight
  problem with a `--public-ip` escape hatch. The 256 byte read limit means an HTML page
  cannot accidentally parse as an address.
- **Reachability never reports unverified as closed. Confirmed.** `blockedSetupPorts`
  returns nil unless `Ran` is true, and `unverifiedReachabilityDetail` words the three
  reasons separately. The design is right even though the check is inert (H2).
- **`machine.json` write path. Confirmed correct for the default path.** Same-directory
  temp file plus rename (atomic, no cross-filesystem rename), explicit 0600, chown only
  when the process is root, chown target taken from the same `setupTargetUser()` that
  produced `plan.User`, so the file's owner and the unit's `User=` cannot disagree. The
  file is read back through `vmgateway.ReadMachineFile`, the gateway's own reader, before
  the gateway is restarted. The only gap is the override case (M6).
- **Re-bind preserves the old binding. Confirmed.** `bindSetupVM` prints the previous
  binding, then does the whole device flow, and only writes after
  `validateMachineFile` passes. Any abort before the write (Ctrl-C, denial, expiry,
  transport loss) leaves the existing `machine.json` untouched, and `runSetupVM` still
  prints the summary and returns the bind error.
- **The device flow client. Confirmed on all four points.** It starts at the
  server-supplied `interval` and only falls back when it is absent or nonsensical
  (`devicePollStartInterval`); `slow_down` adds the RFC's 5 seconds, capped at 60
  (`nextDevicePollInterval`); `expired_token` and `invalid_grant` both route to an offered
  restart, bounded at two; `access_denied` is never retried. It waits before the first poll,
  per RFC 8628 section 3.4. Bounded consecutive transport failures at 5.
- **The device code is never printed, logged, or persisted. Confirmed.**
  `renderDeviceInstructions` prints only `user_code` and the two verification URLs;
  `verification_uri_complete` carries the user code, not the device code. The device code
  travels in a POST body, never a query string. `deviceTokenResponse` deliberately does not
  decode the access token, so it cannot be logged by accident.
- **No secrets in argv or shell history. Confirmed.** Every `runSetupPrivileged` call site
  passes only paths, unit names, package names, and `DEBIAN_FRONTEND=noninteractive`.
  Nothing sensitive is ever an argument to a privileged command, so nothing is visible in
  `ps`.
- **The gateway user can write the ACME cert directory. Confirmed.**
  `plan.CertDir = <DataDir>/vm-gateway/certs` is created by `install -d -m 0700 -o
  plan.User`, the gateway unit runs as `plan.User`, `AO_VM_CERT_DIR` is set explicitly in
  the unit, `vmgateway.Resolve` reads it, and `NewServer` `MkdirAll`s it before binding any
  port. `autocert.DirCache` writes there in-process, and the gateway holds :80 for the
  whole HTTP-01 lifetime, so the 60 day renewal has both the permission and the challenge
  listener it needs. This one is fine.
- **Paths are absolute and explicit. Confirmed.** A relative `AO_DATA_DIR`, `AO_RUN_FILE`,
  or `AO_MACHINE_FILE` is rejected rather than absolutized, with the systemd rationale
  spelled out. The unit environment sets `HOME`, `AO_DATA_DIR`, `AO_RUN_FILE`, and for the
  gateway `AO_MACHINE_FILE`, `AO_VM_DOMAIN`, `AO_VM_CERT_DIR`. `slashPath` uses `path`, not
  `filepath`, so the Windows leg of CLI E2E cannot mangle a Linux path.
- **Unit ordering and separation. Confirmed.** Two units, never collapsed; the gateway has
  `After=network-online.target ao-daemon.service`, the daemon has no capabilities and no
  public port, the gateway has `CAP_NET_BIND_SERVICE` with a matching
  `CapabilityBoundingSet` and `NoNewPrivileges=yes`. `Restart=on-failure` is right for a
  process that exits 0 on SIGTERM.
- **Domain normalization agrees with the gateway. Confirmed.** `normalizeSetupDomain`
  rejects a port, a path, a bare IP, and a hostname with no dot;
  `vmgateway.normalizeDomain` reduces `machine.json`'s origin the same way, and the
  reasoning about `autocert.HostWhitelist` silently ignoring an unparseable host is right.

Desktop

- **`safeStorage` fails closed. Confirmed.** `writeStoredAccount` refuses to write when
  `isEncryptionAvailable()` is false rather than falling back to plaintext, and the check
  is also made before the browser is opened, so a user does not complete a Google login
  only to have it thrown away. When availability flips to false after a token was stored,
  `readStoredAccount` throws and `currentState` reports `unavailable` with the reason; on a
  decrypt failure it reports `signed-out` with the reason. Neither path deletes the file,
  so a re-locked or re-keyed keychain that comes back recovers without a re-login. This is
  the right shape.
- **The loopback listener. Confirmed on binding and shutdown.** `server.listen(0,
  "127.0.0.1", ...)` is loopback-only, never `0.0.0.0`. `state` is validated before the
  code is read (`parseCallback` checks it first). The listener accepts exactly one callback
  (`done` guard), and `close()` runs on success, on error, on the 5 minute timeout, and in
  the `finally` of `runDesktopLogin`, with `closeAllConnections()` so a browser keep-alive
  cannot hold the port. An abandoned login leaves nothing bound. The timer is `unref`ed so
  it never holds app exit. PKCE is S256 from 32 CSPRNG bytes, `plain` is unreachable, and
  the module logs nothing. The one defect is the abort-on-mismatch behaviour (M1).
- **The refresh rotation ordering claim is true (M9 covers the residual).**
- **The local-daemon spawn guard is structural, as claimed. Confirmed.**
  `remoteDaemonLifecycle.start` is `applyRemoteStatus(setStatus) ?? localStart()`, and
  `activeMachineStatus()` returns a non-null status for all three reachability values, so
  `localStart` is not reached rather than being skipped by a condition. `startLocalDaemon`
  and `startDaemonInner` have no other callers in `main.ts`; `refresh` and `stop` are gated
  the same way. `restore()` is awaited before `createWindow()` and before the first
  `startDaemon()`, so a machine remembered from a previous run is in force before anything
  can spawn a local daemon. Sign-out falls back to local through
  `aoMachines().reset()`, and switching to `local` clears `activeMachineStatus` so the
  local path resumes normally.
- **In-flight SSE is torn down on a machine switch. Confirmed.** Both
  `event-transport.ts` and `notifications.ts` subscribe to `subscribeApiBaseUrl` and
  compare the recorded `sourceBaseUrl`, closing and reopening on a change. The residual
  problem is the query cache, not the streams (M4).
- **Hard rules intact. Confirmed.** `main.ts:102` still pins
  `app.setPath("userData", path.join(os.homedir(), ".ao", "electron"))`, with the
  crash-dumps comment above it. Both new state files (`ao-account.json`, `ao-machine.json`)
  are written to `path.dirname(runFilePath())`, which resolves under `~/.ao`. Nothing
  reaches for an OS default app-data location. `AO_REMOTE_URL`/`AO_REMOTE_TOKEN` still
  work and still take precedence: `currentStatus` checks `config` before
  `activeMachineStatus()`, and `readRemoteDaemonConfig`'s validation is unchanged.
- **Token audiences match the contract. Confirmed.** The refresh token is presented at
  exactly one place, `POST /api/v1/token` (`ao-control-token.ts:77`), and the resulting
  control-plane-audience token is used only for `GET /api/v1/machines`
  (`ao-machines.ts:210`). No machine-audience token is minted or cached anywhere in the
  desktop yet, which matches `docs/desktop-login-contract.md:86-87`. Sign-out clears both
  the on-disk refresh token and the in-memory access token.
- **No URL and no token can be typed into the app. Confirmed.** The preload surface exposes
  only `getState`, `refresh`, and `select(machineId)`; the renderer picks an id out of a
  list the main process fetched.
- **Renderer conformance.** `MachinesSection` and `AccountSection` are built from the
  existing `components/ui` primitives (`Badge`, `Button`, `SettingsSection`), lucide icons,
  and the established `settings-row-bar` / `text-settings-*` tokens. Nothing diverges from
  agent-orchestrator that I can see. No findings.

`#20` scheme-aware stripping (the parts that are correct)

- `sanitizeOriginURL` is byte-correct on the cases that matter: an https remote with no
  userinfo, including query, fragment, and trailing slash, is returned verbatim through the
  no-`@` fast path; scp-like `git@host:owner/repo.git` is left alone (`url.Parse` gives it
  no scheme); `ssh://git@host:2222/...` keeps the login name and port and only a password
  would be dropped; `https://user:token@host/...` and bare-username-over-https are both
  stripped; local and relative paths pass through unchanged. The ssh-versus-https asymmetry
  is deliberate and documented at `clone.go:190-203`, and it is right.
- `cmd.WaitDelay` is set on the only git exec in the package that talks to a remote
  (`clone.go:67-68`), and it is transport-independent because there is no scheme branch,
  so ssh is covered as well as https. Every other git exec in the package is local-only.
- The partial-clone cleanup ordering is correct at the Go level: `os.RemoveAll(dest)`
  (`clone.go:77`) runs after `CombinedOutput` returns, and `Wait` does not return until the
  direct child is reaped; `WaitDelay` bounds the pipe wait, not the reap. The residual race
  is SUSPECTED only: `aoprocess.Command` sets no process group, so on a context timeout
  only `git clone` is killed and an orphaned `git-index-pack` grandchild could still be
  writing into `dest` when `RemoveAll` starts. Not reproduced;
  `TestCloneRepository_Timeout` stalls before any pack data flows, so it cannot exercise it.
  Fix if wanted: `Setpgid` and kill the group.

Test runs

- `go test ./internal/cli/ ./internal/vmgateway/ ./internal/doctor/`: 446 passed,
  5 failed, 3 skipped. All 5 failures are the known pre-existing `spawn_test.go`
  `HTTP 404` failures, unrelated to this work.
- `go test ./internal/service/project/`: all 147 pass.
- `npx vitest run` over the nine new and changed desktop test files
  (`ao-account`, `ao-account-store`, `ao-pkce`, `loopback-callback`, `ao-control-token`,
  `ao-machines` main and shared, `remote-daemon`, `control-plane`): 84 passed, 0 failed.
  Note that the golden unit assertions at `setupvm_plan_test.go:525`, `:554`, and `:593`
  bake in the quoted `WorkingDirectory`, which is why the test suite is green while C1
  stands.
