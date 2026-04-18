---
"@aoagents/ao-core": patch
"@aoagents/ao-cli": patch
"@aoagents/ao-web": patch
---

Tighten the session lifecycle review follow-ups by debouncing report-watcher reactions, restoring the shared Geist/JetBrains font setup, wiring recovery validation to real agent activity probes, adding direct coverage for `ao report`, activity-signal classification, and dashboard lifecycle audit panels, and fixing the remaining lifecycle-state regressions around legacy merged-session rehydration and malformed canonical payload parsing.
