---
"@aoagents/ao-plugin-agent-codex": patch
"@aoagents/ao-plugin-agent-claude-code": patch
---

Improve Claude Code and Codex session cost estimates to account for cached-token spend, and make Codex restore commands fall back to approval prompts for worker sessions instead of blindly reusing dangerous bypass flags.
