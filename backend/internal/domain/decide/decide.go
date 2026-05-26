// Package decide is the pure DECIDE core: total, deterministic, zero I/O. It
// collapses observed facts (plus the prior detecting/activity memory) into one
// LifecycleDecision. Every function here must remain side-effect free so the
// whole status truth-table can be tested in isolation.
package decide

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Anti-flap tuning. detecting escalates to stuck only after this many
// consecutive unchanged-evidence ticks OR once this much wallclock has elapsed
// since first entering detecting.
const (
	DetectingMaxAttempts = 3
	DetectingMaxDuration = 5 * time.Minute
)

// LifecycleDecision is the output of every decider: the derived display status
// plus the canonical sub-state values to persist, the human-readable evidence,
// and the (possibly updated) detecting memory.
type LifecycleDecision struct {
	Status        domain.SessionStatus
	Evidence      string
	Detecting     *domain.DetectingState
	SessionState  domain.SessionState
	SessionReason domain.SessionReason
	PRState       domain.PRState
	PRReason      domain.PRReason
}

// ProbeInput reconciles runtime + process liveness. A *failed* probe (timeout
// or error) is distinct from a "dead" verdict and must route to detecting,
// never to a death conclusion. KillRequested short-circuits to terminal.
type ProbeInput struct {
	Runtime        domain.RuntimeState
	RuntimeFailed  bool
	Process        ProcessLiveness
	ProcessFailed  bool
	RecentActivity bool
	KillRequested  bool
	Prior          *domain.DetectingState
	Now            time.Time
}

// ProcessLiveness mirrors isProcessRunning's three-valued answer.
type ProcessLiveness string

const (
	ProcessAlive         ProcessLiveness = "alive"
	ProcessDead          ProcessLiveness = "dead"
	ProcessIndeterminate ProcessLiveness = "indeterminate"
)

// OpenPRInput drives the PR pipeline ladder for an open PR.
type OpenPRInput struct {
	CIFailing        bool
	ChangesRequested bool
	Approved         bool
	Mergeable        bool
	ReviewPending    bool
	IdleBeyond       bool // idle past the stuck threshold
	Number           int
	URL              string
}

// DetectingInput feeds the quarantine counter. Evidence is hashed with
// timestamps stripped, so "same ambiguous signal" keeps the counter climbing
// while any real change resets it.
type DetectingInput struct {
	Evidence       string
	ProposedState  domain.SessionState
	ProposedReason domain.SessionReason
	Prior          *domain.DetectingState
	Now            time.Time
}

// ResolveProbeDecision reconciles runtime/process liveness into a decision.
//
// The ordering encodes the load-bearing invariants:
//   - an explicit kill short-circuits straight to terminal (the only inferred
//     terminal this decider may reach without quarantine);
//   - a *failed* probe (timeout/error) is never read as death — it routes to
//     detecting, as does any disagreement between the two probes;
//   - only runtime-dead + process-dead + no-recent-activity reaches killed.
func ResolveProbeDecision(in ProbeInput) LifecycleDecision {
	if in.KillRequested {
		return LifecycleDecision{
			Status:        domain.StatusKilled,
			Evidence:      "manual kill requested",
			SessionState:  domain.SessionTerminated,
			SessionReason: domain.ReasonManuallyKilled,
		}
	}

	if in.RuntimeFailed || in.ProcessFailed || in.Runtime == domain.RuntimeProbeFailed {
		ev := fmt.Sprintf("probe_failed runtime=%s runtimeFailed=%t process=%s processFailed=%t",
			in.Runtime, in.RuntimeFailed, in.Process, in.ProcessFailed)
		return detecting(in, domain.ReasonProbeFailure, ev)
	}

	switch in.Runtime {
	case domain.RuntimeAlive:
		if in.Process == ProcessDead {
			// Runtime up but the agent process is gone: probes disagree.
			ev := fmt.Sprintf("disagree runtime=alive process=%s recentActivity=%t", in.Process, in.RecentActivity)
			return detecting(in, domain.ReasonAgentProcessExited, ev)
		}
		return LifecycleDecision{
			Status:        domain.StatusWorking,
			Evidence:      fmt.Sprintf("alive runtime=alive process=%s", in.Process),
			SessionState:  domain.SessionWorking,
			SessionReason: domain.ReasonTaskInProgress,
		}

	case domain.RuntimeExited, domain.RuntimeMissing:
		// Runtime is gone. Death is only concluded when the process is *also*
		// confirmed dead AND nothing has been heard from the agent recently;
		// any other shape is ambiguous and quarantines.
		if in.Process == ProcessAlive || in.RecentActivity {
			ev := fmt.Sprintf("disagree runtime=%s process=%s recentActivity=%t", in.Runtime, in.Process, in.RecentActivity)
			return detecting(in, domain.ReasonRuntimeLost, ev)
		}
		if in.Process == ProcessDead {
			return LifecycleDecision{
				Status:        domain.StatusKilled,
				Evidence:      fmt.Sprintf("dead runtime=%s process=dead recentActivity=false", in.Runtime),
				SessionState:  domain.SessionTerminated,
				SessionReason: domain.ReasonRuntimeLost,
			}
		}
		// Process indeterminate: cannot confirm death, so quarantine.
		ev := fmt.Sprintf("runtime_lost runtime=%s process=%s recentActivity=false", in.Runtime, in.Process)
		return detecting(in, domain.ReasonRuntimeLost, ev)

	default:
		// unknown (not yet probed): ambiguous, never conclude death.
		ev := fmt.Sprintf("runtime_unknown runtime=%s process=%s recentActivity=%t", in.Runtime, in.Process, in.RecentActivity)
		return detecting(in, domain.ReasonRuntimeLost, ev)
	}
}

// ResolveOpenPRDecision walks the PR pipeline ladder. CI failure dominates
// everything, then requested changes, then the approval/merge states, then a
// pending review, then a stalled (idle-beyond-threshold) PR, else plain open.
func ResolveOpenPRDecision(in OpenPRInput) LifecycleDecision {
	base := func(status domain.SessionStatus, prReason domain.PRReason, ss domain.SessionState, sr domain.SessionReason) LifecycleDecision {
		return LifecycleDecision{
			Status:        status,
			SessionState:  ss,
			SessionReason: sr,
			PRState:       domain.PROpen,
			PRReason:      prReason,
		}
	}

	switch {
	case in.CIFailing:
		return base(domain.StatusCIFailed, domain.PRReasonCIFailing, domain.SessionWorking, domain.ReasonFixingCI)
	case in.ChangesRequested:
		return base(domain.StatusChangesRequested, domain.PRReasonChangesRequested, domain.SessionWorking, domain.ReasonResolvingReviewComments)
	case in.Mergeable:
		// Mergeability is the authoritative merge gate, so it already folds in
		// "approved if review is required". Checking it before Approved means a
		// PR on a no-required-review repo (mergeable, not formally approved) is
		// still surfaced as ready-to-merge instead of falling through to PR_OPEN.
		return base(domain.StatusMergeable, domain.PRReasonMergeReady, domain.SessionIdle, domain.ReasonAwaitingExternalReview)
	case in.Approved:
		return base(domain.StatusApproved, domain.PRReasonApproved, domain.SessionIdle, domain.ReasonAwaitingExternalReview)
	case in.ReviewPending:
		return base(domain.StatusReviewPending, domain.PRReasonReviewPending, domain.SessionIdle, domain.ReasonAwaitingExternalReview)
	case in.IdleBeyond:
		// A PR open but quiet past the stuck threshold needs a human nudge.
		return base(domain.StatusStuck, domain.PRReasonInProgress, domain.SessionStuck, domain.ReasonAwaitingUserInput)
	default:
		return base(domain.StatusPROpen, domain.PRReasonInProgress, domain.SessionWorking, domain.ReasonPRCreated)
	}
}

// ResolveTerminalPRStateDecision handles merged/closed PRs. A merge parks the
// session idle awaiting a human's post-merge decision; a close drops to idle.
// none/open are not terminal — callers should route those to the open-PR or
// probe deciders — but the function stays total for safety.
func ResolveTerminalPRStateDecision(pr domain.PRState) LifecycleDecision {
	switch pr {
	case domain.PRMerged:
		return LifecycleDecision{
			Status:        domain.StatusMerged,
			Evidence:      "pr merged",
			SessionState:  domain.SessionIdle,
			SessionReason: domain.ReasonMergedWaitingDecision,
			PRState:       domain.PRMerged,
			PRReason:      domain.PRReasonMerged,
		}
	case domain.PRClosed:
		return LifecycleDecision{
			Status:        domain.StatusIdle,
			Evidence:      "pr closed unmerged",
			SessionState:  domain.SessionIdle,
			SessionReason: domain.ReasonAwaitingUserInput,
			PRState:       domain.PRClosed,
			PRReason:      domain.PRReasonClosedUnmerged,
		}
	default:
		return LifecycleDecision{
			Status:        domain.StatusWorking,
			Evidence:      fmt.Sprintf("non-terminal pr state=%s", pr),
			SessionState:  domain.SessionWorking,
			SessionReason: domain.ReasonTaskInProgress,
			PRState:       pr,
		}
	}
}

// CreateDetectingDecision advances or escalates the anti-flap quarantine.
//
// The attempt counter climbs only while the (timestamp-stripped) evidence hash
// is unchanged and resets the moment the evidence moves; StartedAt is preserved
// across the whole detecting episode so the duration cap is a real wall-clock
// safety net even when the evidence keeps flapping. Escalation to stuck fires
// at DetectingMaxAttempts consecutive unchanged ticks OR DetectingMaxDuration
// elapsed since first entering detecting.
func CreateDetectingDecision(in DetectingInput) LifecycleDecision {
	hash := HashEvidence(in.Evidence)

	attempts := 1
	startedAt := in.Now
	if in.Prior != nil {
		startedAt = in.Prior.StartedAt
		if in.Prior.EvidenceHash == hash {
			attempts = in.Prior.Attempts + 1
		}
	}

	escalate := attempts >= DetectingMaxAttempts || !in.Now.Before(startedAt.Add(DetectingMaxDuration))
	if escalate {
		return LifecycleDecision{
			Status:        domain.StatusStuck,
			Evidence:      in.Evidence,
			SessionState:  domain.SessionStuck,
			SessionReason: in.ProposedReason,
		}
	}

	return LifecycleDecision{
		Status:        domain.StatusDetecting,
		Evidence:      in.Evidence,
		Detecting:     &domain.DetectingState{Attempts: attempts, StartedAt: startedAt, EvidenceHash: hash},
		SessionState:  domain.SessionDetecting,
		SessionReason: in.ProposedReason,
	}
}

// HashEvidence normalises an evidence string (stripping timestamps and
// collapsing whitespace) and hashes it, so unchanged-but-restamped signals
// compare equal and the detecting counter is not reset by clock movement alone.
func HashEvidence(evidence string) string {
	s := evidence
	for _, re := range timestampPatterns {
		s = re.ReplaceAllString(s, "")
	}
	s = strings.Join(strings.Fields(s), " ")
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// timestampPatterns strip the time-varying parts of an evidence string before
// hashing. Order matters: the full datetime form is removed before the bare
// time-of-day and epoch forms so they don't partially match.
//
// The epoch pattern strips any bare 10-13 digit run (unix seconds/millis). That
// is broad enough to also clobber a same-width numeric ID if one ever appears in
// an evidence string; evidence is decider-authored, so today nothing else lands
// in that range, but keep IDs out of evidence strings to preserve hash fidelity.
var timestampPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?`),
	regexp.MustCompile(`\d{2}:\d{2}:\d{2}(?:\.\d+)?`),
	regexp.MustCompile(`\b\d{10,13}\b`),
}

// detecting builds a quarantine decision from a probe verdict, threading the
// prior counter through CreateDetectingDecision so probe ambiguity is subject
// to the same anti-flap escalation as any other detecting cause.
func detecting(in ProbeInput, reason domain.SessionReason, evidence string) LifecycleDecision {
	return CreateDetectingDecision(DetectingInput{
		Evidence:       evidence,
		ProposedState:  domain.SessionDetecting,
		ProposedReason: reason,
		Prior:          in.Prior,
		Now:            in.Now,
	})
}
