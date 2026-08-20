# Phase 2 installer: end-to-end acceptance on vm-shared (2026-08-20)

Recording Task 5's live run per `docs/superpowers/plans/2026-08-20-phase-2-installer.md`
Step 6. No passcodes or full `ao-pair://` pairing strings appear below; certificate
fingerprints are public (they are exactly what a client compares by eye during
trust-on-first-use pairing) and are fine to record.

**Target:** `azureuser@20.197.63.75` (Ubuntu, Azure VM, "vm-shared"), `azureuser` has
NOPASSWD sudo.

**Binary:** `ao` 0.13.1, commit `a3341eb`.

## What happened

1. `curl -fsSL https://raw.githubusercontent.com/agentlab-in/hosted-ao/develop/install.sh | sh`
   ran to completion: it downloaded the 0.13.1 Linux binary, execed `sudo ao pair`, and
   provisioning minted the pair-mode certificate and passcode without error. Preflight,
   package installs, and both systemd units writing out all succeeded.
2. `ao-gateway.service` (`User=azureuser`) then crash-looped. `journalctl -u
   ao-gateway.service` showed:

   ```
   read passcode store /home/azureuser/.ao/hosted/vm-gateway/pair-passcode/passcode.hash: permission denied
   ```

3. Root-caused to the bug this record's branch fixes
   (`fix/pair-provision-parent-chown`): provisioning's `LEAF` directories and files
   (`pair-cert/`, `pair-passcode/`, `cert.pem`, `key.pem`, `passcode.hash`) were correctly
   chowned to `azureuser`, mode `0700`/`0600`, but the intermediate directory
   `/home/azureuser/.ao/hosted/vm-gateway` was left `root:root` by the sudo'd
   provisioning run's own directory creation, which the leaf-only chown never reached.
   `azureuser` therefore could not traverse into its own state directory at all.
   `~/.ao/hosted` itself was already `azureuser`-owned from a prior run and was not
   affected; only the newly created `vm-gateway` intermediate was.
4. Manual remediation applied on the box to unblock the acceptance run:
   `sudo chown azureuser:azureuser /home/azureuser/.ao/hosted/vm-gateway`, followed by
   `sudo systemctl restart ao-gateway.service`.
5. After remediation, `sudo ao vm rotate-passcode` printed a fresh, valid `ao-pair://v1/`
   string. Its embedded certificate fingerprint was checked byte-for-byte against an
   independent `openssl s_client -connect 20.197.63.75:443 -showcerts` probe's own SHA-256
   fingerprint of the presented leaf certificate: the two matched exactly, confirming the
   gateway is serving the same certificate the pairing string pins, over the real public
   listener.
6. Final state: `systemctl status ao-daemon.service ao-gateway.service` on the box showed
   both units `active (running)`.

## Fix

`fix/pair-provision-parent-chown` (this branch): `ao pair` / `ao setup-vm --pair`
provisioning now snapshots which ancestor directories do not exist yet immediately before
`vmgateway.GeneratePasscode` / `vmgateway.LoadOrCreatePairCertificate` create them (each
calls `os.MkdirAll` internally, which mints every missing directory in the path under the
process's own root euid when run via sudo), and chowns exactly those directories to the
target user afterward, in addition to the existing leaf-tree chown. Directories that
already existed before provisioning ran are never touched. See `backend/internal/cli/setupvm.go`
(`setupMissingSetupDirs`, `chownSetupDirs`) and its tests in `setupvm_test.go` /
`setupvm_unix_test.go`.

Same branch also fixes a related UX bug this incident exposed: non-root `ao pair show`
against a provisioned-but-unreadable-by-this-uid box previously suggested "run bare
`ao pair`", which is wrong twice (the box is provisioned, and a non-root bare `ao pair`
hits the identical permission error). It now distinguishes a permission error from
not-exist and suggests `sudo ao pair show` instead. See `backend/internal/cli/pair.go`
(`pairPasscodeReadError`, `pairIsProvisioned`).

## Re-verification

Re-running the installer against a fresh state root with the fix applied is covered by
this branch's own tests (`TestSetupMissingSetupDirs_*`, `TestEnsureSetupPasscode_ChownsTheIntermediateDirItCreates`
in `backend/internal/cli`), which assert every directory a sudo'd provisioning run creates
gets chowned, not only the leaf. A second live installer run against vm-shared with the
fixed binary is the natural follow-up once this PR ships a release, but is not required to
close out Task 5: the crash-loop's root cause is confirmed, the fix is unit-tested, and the
box is healthy under the manual remediation in the interim.
