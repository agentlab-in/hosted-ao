"@aoagents/ao-cli": patch
"@aoagents/ao-web": patch
---

Fix restore behavior across AO session recovery flows.

- restore the latest dead-but-restorable orchestrator on `ao start` instead of silently spawning a new orchestrator when tmux is gone
- make worker session orchestrator navigation prefer the most recently active live orchestrator for the project
