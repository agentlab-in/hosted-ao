package domain

// TrackerProvider identifies an issue-tracker provider implementation.
// Provider differences (label-driven vs state-machine vs close-reason) are
// absorbed inside each adapter; the rest of the system only sees
// NormalizedIssueState.
type TrackerProvider string

const (
	TrackerProviderGitHub TrackerProvider = "github"
	TrackerProviderGitLab TrackerProvider = "gitlab"
	TrackerProviderLinear TrackerProvider = "linear"
)

// TrackerID identifies a single issue across providers. Native is the
// provider's own canonical form ("owner/repo#123" for GitHub,
// "group/project#456" for GitLab, "ABC-789" for Linear) and is parsed by the
// adapter. Provider is the discriminator the Session Manager uses to pick an
// adapter.
type TrackerID struct {
	Provider TrackerProvider `json:"provider"`
	Native   string          `json:"native"`
}

// NormalizedIssueState is the cross-provider issue-state vocabulary every
// adapter must implement. The closed list is intentional — adding a value
// here is a port-level decision because every adapter must map it.
type NormalizedIssueState string

const (
	IssueOpen       NormalizedIssueState = "open"
	IssueInProgress NormalizedIssueState = "in_progress"
	IssueInReview   NormalizedIssueState = "review"
	IssueDone       NormalizedIssueState = "done"
	IssueCancelled  NormalizedIssueState = "cancelled"
)

// Issue is the minimum projection every tracker can produce. Fields are
// added only when all v1 providers (GitHub, GitLab, Linear) can populate
// them faithfully; richer metadata stays inside provider-specific code paths.
type Issue struct {
	ID        TrackerID            `json:"id"`
	Title     string               `json:"title"`
	Body      string               `json:"body"`
	State     NormalizedIssueState `json:"state"`
	URL       string               `json:"url"`
	Labels    []string             `json:"labels,omitempty"`
	Assignees []string             `json:"assignees,omitempty"`
}
