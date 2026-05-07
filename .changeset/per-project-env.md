---
"@aoagents/ao-core": minor
---

Add optional per-project `env` block to `ProjectConfig` that forwards string-to-string env vars into worker session runtimes (e.g. pin `GH_TOKEN` per project). AO-internal vars (`AO_SESSION`, `AO_PROJECT_ID`, etc.) always take precedence.
