# Hosted AO v1: run coding agents on your own cloud VM

## Intent, stated cleanly

Let a user log into AO with Google, connect a cloud VM they own and pay for, and
run coding agents on that VM from the desktop app. The VM is prepared once with
`ao setup-vm`, which binds it to the user's AO account via a GitHub-style device
code, installs the daemon and TLS edge on a domain the user owns, and registers
the machine so it appears in the app automatically. Harnesses and git are
authenticated on the VM by separate explicit commands. Shipped means: log in,
pick a machine, add a project by git URL, and run a coding agent on it.

## What it is / what it isn't

**In scope**

- AO accounts at `ao.agentlab.in`, Google login.
- `ao setup-vm`, run on the user's VM. Asks for a device code that the user
  generates from the AO web app after logging in. Installs the AO daemon, Caddy
  as TLS edge, tmux, obtains a Let's Encrypt certificate for the user's domain,
  and registers the machine with AO. `ao whoami` verifies the binding.
- A machine registry: after Google login the desktop app lists the user's
  registered machines and the user clicks one. No URL or token pasting in the
  app.
- Auth by short-lived AO-signed tokens. The VM verifies them against AO's public
  keys and against an account allowlist written onto the box at setup time.
- `ao vm setup-harness <harness>` as a separate interactive command per harness,
  since more than one harness exists and each has its own auth. It carries the
  user through the harness's own URL-plus-code exchange.
- Git access via the user's own GitHub identity, using the `gh auth login`
  device flow on the VM.
- Adding a project by pasting a git URL, which AO clones on the VM.

**Explicitly out of scope**

- Moving sessions between machines, backing sessions up to AO Cloud, and
  VM-to-VM transfer. Parked, not dead. If a VM is destroyed, whatever was on it
  is lost, and that is accepted for v1.
- AO provisioning VMs, or AO handing out subdomains like
  `username.ao.agentlab.in`. The user brings a VM and a domain and makes their
  own DNS change.
- Connecting by bare IP address.
- A GitHub App. Parked.
- Browser-preview proxying, already out of scope in the current hosted slice.

## Assumptions surfaced and confirmed

- The VM is the user's property and expense. AO never owns or operates it.
- The user owns a domain and will add a DNS record pointing at the VM. This is
  non-negotiable, because a bare IP cannot get a trusted certificate and the
  whole transport, including the Secure HttpOnly cookie scheme, depends on TLS.
- AO sits in the login and discovery path only, never in the data path. AO
  stores no credential that opens any user's machine. If AO is down,
  already-connected users are affected only insofar as their tokens expire.
- Harness authentication is a genuine two-way exchange (a URL to open plus a
  code to paste back), so it must run interactively and cannot be a
  fire-and-forget install step.
- A broad-scoped GitHub user token will live on a cloud VM. Accepted for v1 in
  exchange for correct authorship and zero implementation cost.
- Commit authorship comes from `git config`, not from whichever token pushes, so
  agent commits look like the user's regardless of the credential path.
- A registered machine can exist with no harness configured. Existing harness
  login checks are considered sufficient to surface that state.
- The honest v1 flow costs one SSH session, one DNS record, and two browser
  round trips (AO device code, then harness auth). "Super fast" as the original
  framing is retired: this is a one-time setup comparable to Tailscale or a
  self-hosted CI runner, after which connecting is instant.

## Alternatives considered and rejected

- **No accounts, paste a URL and a token the VM printed.** Cheaper by days.
  Rejected because accounts are wanted in place before billing and before
  session migration land, and the registry it enables removes all pasting from
  the app.
- **AO stores a per-machine secret and hands it to the app after login.** The
  smallest change from the current shared-secret gate. Rejected because it would
  give AO a working key to every user's VM, which contradicts the stated
  instinct that some credentials must never reach the cloud, and would make a
  breach of AO a breach of every customer's machine.
- **Registry as a binding record only, with the user still typing the domain in
  the app.** Rejected: the registry is being paid for either way, so discovery
  is free once it exists.
- **AO-issued subdomains under `ao.agentlab.in`.** Better UX, seriously
  considered, rejected for v1 because it makes AO a DNS operator for users and
  drags AO toward owning user infrastructure.
- **Self-signed certificates with pinning in Electron, or an outbound tunnel.**
  Rejected in favour of requiring a user-owned domain. Pinning works only in the
  desktop app and never in a browser.
- **Adding a project by absolute VM path, or by a remote file browser.**
  Rejected because both put an SSH session or extra UI back into the happy path
  that the git-URL clone removes.
- **A GitHub App.** Buys per-repo scoping and revocability, which will matter
  later. Rejected for v1 as weeks of work, and its installation-token path would
  attribute pull requests to `ao[bot]` rather than the user.

## Small calls deferred

- Token lifetime, refresh, and clock-skew tolerance for the AO-signed tokens.
- Exact copy and affordance shown when a machine has no harness configured.
- Machine revocation and removal UX.
- Whether git setup gets its own `ao vm setup-git` wrapper or is a documented
  `gh auth login` step.
- Multi-machine list ordering and naming in the app.
