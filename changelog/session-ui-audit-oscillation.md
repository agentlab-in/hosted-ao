# Session UI, Report Audit, and Lifecycle Stability

## What Changed

- Split worker session detail from orchestrator detail so orchestrator pages no longer show worker-only PR and lifecycle detail panels.
- Added a persistent audit trail for `ao acknowledge` and `ao report`, including actor, command, note, acceptance/rejection, and before/after lifecycle state.
- Exposed that audit trail on worker session detail pages in the dashboard.
- Fixed lifecycle fallback so `needs_input` and `stuck` do not bounce back to `working` when the poll cycle only has weak or unchanged evidence.
- Removed the web app's runtime dependency on Google Fonts during `next build` by using local CSS font variables instead of `next/font/google`.
