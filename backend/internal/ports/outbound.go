package ports

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// LifecycleStore is the persistence adapter, the ONLY disk writer. It owns
// merge-patch, atomic write, file lock, and CDC eventing. The LCM and SM only
// ever touch state through this narrow interface.
//
// List returns persistence records (no derived status); the Session Manager
// turns those into domain.Session by attaching the derived display status.
//
// Seed and Get are the two record-with-identity methods the Session Manager
// needs that the LCM does not: Load returns lifecycle only (all the decider
// needs), so the SM read-model and explicit-create path would otherwise have no
// way to write or read a record's identity (ID/ProjectID/IssueID/Kind/CreatedAt)
// by id. (Co-owned with Tom's persistence layer — added here to close that gap.)
type LifecycleStore interface {
	Load(ctx context.Context, id domain.SessionID) (domain.CanonicalSessionLifecycle, bool, error)
	PatchLifecycle(ctx context.Context, id domain.SessionID, patch LifecyclePatch) error
	List(ctx context.Context, project domain.ProjectID) ([]domain.SessionRecord, error)
	GetMetadata(ctx context.Context, id domain.SessionID) (map[string]string, error)
	PatchMetadata(ctx context.Context, id domain.SessionID, kv map[string]string) error

	// Seed creates a new record with its identity and initial lifecycle. It is
	// the SM's explicit-create path (the LCM only ever patches existing records);
	// OnSpawnCompleted requires a seeded record, so Spawn calls this first. It
	// must reject a seed for an id that already exists rather than overwrite —
	// re-seeding an existing session (e.g. Restore) goes through PatchLifecycle.
	Seed(ctx context.Context, rec domain.SessionRecord) error

	// Get returns a single full record (with identity) by id. Load is
	// lifecycle-only, so the SM uses this to build the read-model and to
	// reconstruct teardown handles for Kill/Restore on one id.
	Get(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
}

// LifecyclePatch is a sparse merge-patch: a nil field is left untouched, a
// non-nil field is written.
//
// Detecting needs three-way semantics (leave / set / clear-to-nil):
//   - ClearDetecting == true  → store clears the detecting memory and IGNORES
//     the Detecting field (clear wins; setting both is a caller bug).
//   - ClearDetecting == false, Detecting != nil → set/replace the memory.
//   - ClearDetecting == false, Detecting == nil  → leave it untouched.
//
// ExpectedRevision supports optimistic concurrency: when non-nil the store must
// reject the patch if the stored Revision (the monotonic write counter, NOT the
// schema Version) differs. This is the alternative to the LCM owning all
// per-session serialisation itself.
type LifecyclePatch struct {
	Session          *domain.SessionSubstate
	PR               *domain.PRSubstate
	Runtime          *domain.RuntimeSubstate
	Activity         *domain.ActivitySubstate
	Detecting        *domain.DetectingState
	ClearDetecting   bool
	ExpectedRevision *int
}

// Notifier delivers events to the human (desktop/Slack later). Push, never pull.
type Notifier interface {
	Notify(ctx context.Context, event OrchestratorEvent) error
}

type EventPriority string

const (
	PriorityUrgent  EventPriority = "urgent"
	PriorityAction  EventPriority = "action"
	PriorityWarning EventPriority = "warning"
	PriorityInfo    EventPriority = "info"
)

type OrchestratorEvent struct {
	Type      string
	Priority  EventPriority
	SessionID domain.SessionID
	ProjectID domain.ProjectID
	Message   string
	Data      map[string]any
}

// AgentMessenger injects a message into a running agent. The implementation
// busy-detects (waits for the agent to be idle/ready) and verifies delivery,
// which is why activity-detection accuracy matters.
type AgentMessenger interface {
	Send(ctx context.Context, id domain.SessionID, message string) error
}

// The runtime/agent/workspace plugin ports are co-owned with the coding-agents
// lane; the method sets below are the minimum the Session Manager spawn/kill
// pipelines call. They will be fleshed out alongside the tmux/claude-code impls.

type Runtime interface {
	Create(ctx context.Context, cfg RuntimeConfig) (RuntimeHandle, error)
	Destroy(ctx context.Context, handle RuntimeHandle) error
	SendMessage(ctx context.Context, handle RuntimeHandle, message string) error
	GetOutput(ctx context.Context, handle RuntimeHandle, lines int) (string, error)
	IsAlive(ctx context.Context, handle RuntimeHandle) (bool, error)
}

type RuntimeConfig struct {
	SessionID     domain.SessionID
	WorkspacePath string
	LaunchCommand string
	Env           map[string]string
}

type RuntimeHandle struct {
	ID          string
	RuntimeName string
}

type Agent interface {
	GetLaunchCommand(cfg AgentConfig) string
	GetEnvironment(cfg AgentConfig) map[string]string
	// ProbeProcess returns the agent process liveness classification
	// (alive/dead/indeterminate/failed) — not a boolean and not an activity
	// state. Activity classification arrives separately via ActivitySignal.
	ProbeProcess(ctx context.Context, handle RuntimeHandle) (ProcessProbe, error)
	GetRestoreCommand(agentSessionID string) string
}

type AgentConfig struct {
	SessionID     domain.SessionID
	WorkspacePath string
	Prompt        string
}

type Workspace interface {
	Create(ctx context.Context, cfg WorkspaceConfig) (WorkspaceInfo, error)
	Destroy(ctx context.Context, info WorkspaceInfo) error
	List(ctx context.Context, project domain.ProjectID) ([]WorkspaceInfo, error)
	Restore(ctx context.Context, cfg WorkspaceConfig) (WorkspaceInfo, error)
}

type WorkspaceConfig struct {
	ProjectID domain.ProjectID
	SessionID domain.SessionID
	Branch    string
}

type WorkspaceInfo struct {
	Path      string
	Branch    string
	SessionID domain.SessionID
	ProjectID domain.ProjectID
}
