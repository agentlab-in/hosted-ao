---
"@aoagents/ao": minor
"@aoagents/ao-cli": minor
"@aoagents/ao-web": minor
"@aoagents/ao-plugin-agent-grok": minor
---

Load agent-grok package metadata through JSON import attributes so packaged web and CLI runtimes do not keep a publish-host package.json lookup. This also raises the Node.js engine floor to 20.18.3+, where JSON modules with import attributes are non-experimental.
