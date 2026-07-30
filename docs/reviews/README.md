# Preserved code-review reports

Read-only review reports, copied into the repo because they were written to `/tmp` and are
the only record of what was found, what was **confirmed** versus **suspected**, and what was
read and found correct and therefore does not need re-reviewing.

Each file carries a header giving the reviewed commit, the date, the reviewer session, and a
table mapping every finding to the PR that fixed it. Everything below each header's
horizontal rule is the report exactly as written.

| Report | Scope | Reviewed at | Findings |
| --- | --- | --- | --- |
| [2026-07-30-review-batches-1-2.md](2026-07-30-review-batches-1-2.md) | Hosted AO v1 batches 1 and 2: control plane skeleton, ADR 0002, `cloneUrl`, Google login, keys and tokens, `ao vm serve` | `193f6cc52` | 17 |
| [2026-07-30-review-batches-3-4-server.md](2026-07-30-review-batches-3-4-server.md) | Batches 3 and 4, server half: control plane, gateway, tokens, daemon HTTP | `a7eb9c6b2` | 15 |
| [2026-07-30-review-batches-3-4-client.md](2026-07-30-review-batches-3-4-client.md) | Batches 3 and 4, client half: CLI, `ao setup-vm`, desktop auth and machines | `a7eb9c6b2` | 22 |

Section 3 of the client-side report is the ranked pre-flight risk list for the fresh-VM run
(spec task 14), which has not happened yet.

For the decisions behind the work these reviews cover, see
[../hosted-ao-v1-build-log.md](../hosted-ao-v1-build-log.md).
