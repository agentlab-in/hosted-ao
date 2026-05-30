// Package github implements the ports.Tracker outbound port for GitHub
// Issues. v1 is read-only: Get returns a normalized Issue snapshot that the
// Session Manager uses to hydrate the agent prompt during spawn-bootstrap.
// Writing back to the tracker (Comment, Transition) is deferred to issue
// #40; the observer/polling loop is deferred to issue #35.
//
// # Reverse state mapping
//
// GitHub Issues only have two native states (open, closed) plus a
// state_reason on closed issues (completed, not_planned, reopened). Get
// projects them onto the normalized state vocabulary as follows:
//
//   - closed + state_reason=not_planned       -> cancelled
//   - closed + (completed | empty | other)    -> done
//   - open   + "in-review" label               -> review        (wins when
//     both status labels are present; the workflow is progress -> review)
//   - open   + "in-progress" label             -> in_progress
//   - otherwise                                -> open
//
// The "in-progress" and "in-review" labels are recognized because humans
// (and other tooling) commonly apply them. The adapter does NOT write them
// in v1 — see issue #40 for the write-side work.
//
// # Out of scope
//
//   - No Comment, no Transition (issue #40).
//   - No webhook receiver, no polling goroutine, no fact projection into
//     LCM (issue #35).
//   - No richer per-provider metadata on Issue (milestones, project boards,
//     reactions); the port only carries fields all v1 providers can fill.
package github
