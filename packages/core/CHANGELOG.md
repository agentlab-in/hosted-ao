# @composio/ao-core

## Unreleased

### Patch Changes

- Fix lifecycle activity inference to model missing evidence explicitly, prevent null or failed probes from being treated as idle proof, and require valid idle timing before sessions can transition to `stuck`.

## 0.2.0

### Minor Changes

- 3a650b0: Zero-friction onboarding: `ao start` auto-detects project, generates config, and launches dashboard — no prompts, no manual setup. Renamed npm package to `@composio/ao`. Made `@composio/ao-web` publishable with production entry point. Cross-platform agent detection. Auto-port-finding. Permission auto-retry in shell scripts.
