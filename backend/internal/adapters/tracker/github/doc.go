// Package github implements the ports.Tracker outbound port for GitHub
// Issues. v1 is write-mostly: Get returns a normalized Issue snapshot,
// Comment posts an issue comment, and Transition projects the cross-provider
// state vocabulary onto GitHub's open/closed + state_reason + labels surface.
// There is no observer loop or cache — those arrive with issue #35.
//
// # Normalized state mapping
//
// GitHub Issues only have two native states (open, closed) plus a
// state_reason on closed issues (completed, not_planned, reopened). The
// orchestrator's lifecycle vocabulary is richer, so the adapter uses two
// well-known labels — "in-progress" and "in-review" — to project the extra
// states onto open issues.
//
//	Normalized state | GitHub API calls performed by Transition
//	-----------------+-------------------------------------------------------
//	open             | PATCH state=open; DELETE labels {in-progress,in-review}
//	in_progress      | PATCH state=open; POST   label  in-progress;
//	                 | DELETE label in-review
//	review           | PATCH state=open; POST   label  in-review;
//	                 | DELETE label in-progress
//	done             | PATCH state=closed,state_reason=completed;
//	                 | DELETE labels {in-progress,in-review}
//	cancelled        | PATCH state=closed,state_reason=not_planned;
//	                 | DELETE labels {in-progress,in-review}
//
// Reverse mapping (Get): GitHub state=closed maps to done if state_reason is
// completed or empty, and to cancelled if state_reason is not_planned. For
// open issues, an "in-review" label wins over "in-progress" (the workflow is
// progress -> review -> done), and the absence of both maps to open.
//
// # Label hygiene and partial failures
//
// DELETE on a label that the issue does not carry returns 404; Transition
// treats that as success so the operation is idempotent.
//
// Transition issues 2-3 HTTP requests sequentially (PATCH, optional POST
// label, DELETE label) and is NOT atomic. If the PATCH succeeds but a
// subsequent label call fails, the issue is left in an intermediate state
// (e.g. closed without the status label cleared). Re-invoking Transition
// with the same target state is safe and converges — callers should treat
// the operation as eventually-consistent and retry on transport errors.
//
// # Out of scope
//
//   - No webhook receiver, no polling goroutine, no fact projection into LCM
//     (see issue #35 for the observer-loop work).
//   - No richer per-provider metadata on Issue (milestones, project boards,
//     reactions); the port only carries fields all three v1 providers can fill.
package github
