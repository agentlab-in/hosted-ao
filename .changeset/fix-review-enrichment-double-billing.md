---
"@aoagents/ao-core": patch
---

fix(core): prevent double-billing reaction attempts on changes_requested transition

The enriched review dispatch in `maybeDispatchReviewBacklog` now sends directly via
`sessionManager.send` when the transition handler already called `executeReaction` for
the same reaction key. This prevents the attempt counter from incrementing twice in a
single poll cycle, which would cause premature escalation for projects with `retries: 1`.

Also moves the review backlog throttle timestamp after the SCM fetch so a failed
`getReviewThreads` call doesn't block retries for 2 minutes.
