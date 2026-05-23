---
"@aoagents/ao": patch
"@aoagents/ao-cli": patch
"@aoagents/ao-web": patch
"@aoagents/ao-plugin-agent-grok": patch
---

Load agent-grok package metadata through JSON import attributes so packaged web and CLI runtimes do not keep a publish-host package.json lookup. This also raises the Node.js engine floor to 20.18.3+, where JSON modules with import attributes are non-experimental.
