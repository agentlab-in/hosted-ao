// Package decide is the pure DECIDE core: total, deterministic, zero I/O. It
// collapses observed facts (plus the prior detecting/activity memory) into one
// LifecycleDecision. Every function here must remain side-effect free so the
// whole status truth-table can be tested in isolation.
//
// NOTE: function bodies are stubbed in this contracts PR. The real logic + the
// exhaustive truth-table tests land in the follow-up "decide core" PR. The
// signatures and the input/output shapes are what we are stabilising now.
package decide

import (
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
func ResolveProbeDecision(in ProbeInput) LifecycleDecision {
	panic("decide.ResolveProbeDecision: not implemented (decide-core PR)")
}

// ResolveOpenPRDecision walks the PR pipeline ladder.
func ResolveOpenPRDecision(in OpenPRInput) LifecycleDecision {
	panic("decide.ResolveOpenPRDecision: not implemented (decide-core PR)")
}

// ResolveTerminalPRStateDecision handles merged/closed PRs.
func ResolveTerminalPRStateDecision(pr domain.PRState) LifecycleDecision {
	panic("decide.ResolveTerminalPRStateDecision: not implemented (decide-core PR)")
}

// CreateDetectingDecision advances or escalates the anti-flap quarantine.
func CreateDetectingDecision(in DetectingInput) LifecycleDecision {
	panic("decide.CreateDetectingDecision: not implemented (decide-core PR)")
}

// HashEvidence normalises an evidence string (stripping timestamps) and hashes
// it, so unchanged-but-restamped signals compare equal.
func HashEvidence(evidence string) string {
	panic("decide.HashEvidence: not implemented (decide-core PR)")
}
