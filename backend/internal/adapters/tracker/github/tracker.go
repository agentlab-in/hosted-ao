package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	defaultBaseURL   = "https://api.github.com"
	defaultUserAgent = "ao-agent-orchestrator/tracker-github"

	labelInProgress = "in-progress"
	labelInReview   = "in-review"

	stateOpenGH    = "open"
	stateClosedGH  = "closed"
	reasonComplete = "completed"
	reasonNotPlan  = "not_planned"
)

// Sentinel errors. Adapter-level callers should match on these via
// errors.Is; the orchestrator's lifecycle code is intentionally insulated
// from raw HTTP status codes.
var (
	ErrNotFound      = errors.New("github tracker: issue not found")
	ErrRateLimited   = errors.New("github tracker: rate limited")
	ErrEmptyBody     = errors.New("github tracker: comment body is empty")
	ErrWrongProvider = errors.New("github tracker: id is not a github tracker id")
	ErrUnknownState  = errors.New("github tracker: unknown normalized state")
	ErrBadID         = errors.New("github tracker: malformed native id")
)

// RateLimitError is returned when GitHub reports the request was rate-limited.
// Callers that want to back off intelligently can extract ResetAt /
// RetryAfter via errors.As; callers that only need the category can use
// errors.Is(err, ErrRateLimited).
type RateLimitError struct {
	ResetAt    time.Time
	RetryAfter time.Duration
	Message    string
}

func (e *RateLimitError) Error() string {
	if e == nil {
		return ErrRateLimited.Error()
	}
	if e.Message != "" {
		return "github tracker: rate limited: " + e.Message
	}
	return ErrRateLimited.Error()
}

func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// Options configures a Tracker. All fields except Token are optional —
// production code typically sets Token alone; tests inject HTTPClient and
// BaseURL to point at an httptest fake.
type Options struct {
	Token      TokenSource
	HTTPClient *http.Client
	BaseURL    string
	UserAgent  string
}

// Tracker implements ports.Tracker against the GitHub REST API.
//
// Construction performs a fail-fast token presence check (no network call —
// validating the token's authorization scope against GitHub requires a real
// request, and that is the first operation any caller will make anyway).
type Tracker struct {
	http      *http.Client
	tokens    TokenSource
	baseURL   string
	userAgent string
}

// New returns a Tracker. It fails fast when no token can be obtained so
// daemons crash at startup rather than at first issue lookup.
func New(opts Options) (*Tracker, error) {
	src := opts.Token
	if src == nil {
		return nil, ErrNoToken
	}
	if _, err := src.Token(context.Background()); err != nil {
		return nil, err
	}
	t := &Tracker{
		http:      opts.HTTPClient,
		tokens:    src,
		baseURL:   opts.BaseURL,
		userAgent: opts.UserAgent,
	}
	if t.http == nil {
		t.http = &http.Client{Timeout: 30 * time.Second}
	}
	if t.baseURL == "" {
		t.baseURL = defaultBaseURL
	}
	if t.userAgent == "" {
		t.userAgent = defaultUserAgent
	}
	return t, nil
}

// Statically assert Tracker satisfies the port. If this stops compiling, the
// port shape changed and the adapter needs to follow.
var _ ports.Tracker = (*Tracker)(nil)

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

// ghIssue is the subset of fields we read off the REST issue payload.
type ghIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	StateReason string    `json:"state_reason"`
	HTMLURL     string    `json:"html_url"`
	Labels      []ghLabel `json:"labels"`
	Assignees   []ghUser  `json:"assignees"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghUser struct {
	Login string `json:"login"`
}

func (t *Tracker) Get(ctx context.Context, id domain.TrackerID) (domain.Issue, error) {
	owner, repo, number, err := t.parseID(id)
	if err != nil {
		return domain.Issue{}, err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)

	resp, err := t.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return domain.Issue{}, err
	}
	var raw ghIssue
	if err := json.Unmarshal(resp, &raw); err != nil {
		return domain.Issue{}, fmt.Errorf("github tracker: decode issue: %w", err)
	}
	labels := make([]string, 0, len(raw.Labels))
	for _, l := range raw.Labels {
		labels = append(labels, l.Name)
	}
	assignees := make([]string, 0, len(raw.Assignees))
	for _, a := range raw.Assignees {
		assignees = append(assignees, a.Login)
	}
	out := domain.Issue{
		// Canonicalize Provider so the returned Issue always re-routes back
		// to this adapter, even if the caller built id with a zero Provider.
		ID:        domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: id.Native},
		Title:     raw.Title,
		Body:      raw.Body,
		State:     mapStateFromGitHub(raw.State, raw.StateReason, labels),
		URL:       raw.HTMLURL,
		Labels:    labels,
		Assignees: assignees,
	}
	if len(out.Labels) == 0 {
		out.Labels = nil
	}
	if len(out.Assignees) == 0 {
		out.Assignees = nil
	}
	return out, nil
}

// mapStateFromGitHub projects GitHub's open/closed + state_reason + labels
// surface onto the normalized state. "in-review" wins over "in-progress"
// when both labels are present (the workflow is progress -> review -> done).
func mapStateFromGitHub(state, reason string, labels []string) domain.NormalizedIssueState {
	switch strings.ToLower(state) {
	case stateClosedGH:
		if strings.EqualFold(reason, reasonNotPlan) {
			return domain.IssueCancelled
		}
		return domain.IssueDone
	}
	var hasProgress, hasReview bool
	for _, l := range labels {
		switch l {
		case labelInProgress:
			hasProgress = true
		case labelInReview:
			hasReview = true
		}
	}
	switch {
	case hasReview:
		return domain.IssueInReview
	case hasProgress:
		return domain.IssueInProgress
	default:
		return domain.IssueOpen
	}
}

// ---------------------------------------------------------------------------
// Comment
// ---------------------------------------------------------------------------

func (t *Tracker) Comment(ctx context.Context, id domain.TrackerID, body string) error {
	if strings.TrimSpace(body) == "" {
		return ErrEmptyBody
	}
	owner, repo, number, err := t.parseID(id)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number)
	_, err = t.do(ctx, http.MethodPost, path, map[string]string{"body": body})
	return err
}

// ---------------------------------------------------------------------------
// Transition
// ---------------------------------------------------------------------------

// transitionPlan is the per-target-state list of mutations to apply. Every
// transition issues exactly one PATCH on the issue, optionally adds one
// status label, and removes any other status labels the issue may carry.
type transitionPlan struct {
	patch       map[string]any
	addLabel    string // "" means none
	removeLabel []string
}

func planForState(state domain.NormalizedIssueState) (transitionPlan, error) {
	switch state {
	case domain.IssueOpen:
		return transitionPlan{
			patch:       map[string]any{"state": stateOpenGH},
			removeLabel: []string{labelInProgress, labelInReview},
		}, nil
	case domain.IssueInProgress:
		return transitionPlan{
			patch:       map[string]any{"state": stateOpenGH},
			addLabel:    labelInProgress,
			removeLabel: []string{labelInReview},
		}, nil
	case domain.IssueInReview:
		return transitionPlan{
			patch:       map[string]any{"state": stateOpenGH},
			addLabel:    labelInReview,
			removeLabel: []string{labelInProgress},
		}, nil
	case domain.IssueDone:
		return transitionPlan{
			patch:       map[string]any{"state": stateClosedGH, "state_reason": reasonComplete},
			removeLabel: []string{labelInProgress, labelInReview},
		}, nil
	case domain.IssueCancelled:
		return transitionPlan{
			patch:       map[string]any{"state": stateClosedGH, "state_reason": reasonNotPlan},
			removeLabel: []string{labelInProgress, labelInReview},
		}, nil
	default:
		return transitionPlan{}, fmt.Errorf("%w: %q", ErrUnknownState, state)
	}
}

func (t *Tracker) Transition(ctx context.Context, id domain.TrackerID, state domain.NormalizedIssueState) error {
	plan, err := planForState(state)
	if err != nil {
		return err
	}
	owner, repo, number, err := t.parseID(id)
	if err != nil {
		return err
	}
	issuePath := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)

	// 1. Patch state (+ state_reason for closed transitions).
	if _, err := t.do(ctx, http.MethodPatch, issuePath, plan.patch); err != nil {
		return err
	}
	// 2. Add the target status label (no-op when target is open/done/cancelled).
	if plan.addLabel != "" {
		body := map[string][]string{"labels": {plan.addLabel}}
		if _, err := t.do(ctx, http.MethodPost, issuePath+"/labels", body); err != nil {
			return err
		}
	}
	// 3. Remove the other status labels. 404 from GitHub means "label is not
	//    on this issue", which is the success state we want — swallow it so
	//    the operation is idempotent.
	for _, label := range plan.removeLabel {
		labelPath := issuePath + "/labels/" + url.PathEscape(label)
		if _, err := t.do(ctx, http.MethodDelete, labelPath, nil); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// HTTP plumbing
// ---------------------------------------------------------------------------

func (t *Tracker) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("github tracker: encode body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("github tracker: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", t.userAgent)
	tok, err := t.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := t.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github tracker: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return respBody, nil
	}
	return respBody, classifyError(resp, respBody)
}

func classifyError(resp *http.Response, body []byte) error {
	msg := githubMessage(body)
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	case http.StatusTooManyRequests:
		return rateLimited(resp, msg)
	case http.StatusForbidden, http.StatusUnauthorized:
		// GitHub returns 403 for primary rate-limit exhaustion, for
		// secondary/abuse limits, and for genuine auth failures. Three
		// signals disambiguate the rate-limit cases from auth: the primary
		// limit sets X-RateLimit-Remaining=0; the secondary/abuse limit
		// sets Retry-After (and often omits X-RateLimit-Remaining); and
		// either case mentions "rate limit" / "abuse" in the body.
		if isRateLimited(resp, msg) {
			return rateLimited(resp, msg)
		}
	}
	return fmt.Errorf("github tracker: %d %s", resp.StatusCode, msg)
}

func isRateLimited(resp *http.Response, msg string) bool {
	if rem := resp.Header.Get("X-RateLimit-Remaining"); rem != "" {
		if n, err := strconv.Atoi(rem); err == nil && n == 0 {
			return true
		}
	}
	if resp.Header.Get("Retry-After") != "" {
		return true
	}
	low := strings.ToLower(msg)
	return strings.Contains(low, "rate limit") || strings.Contains(low, "abuse detection")
}

func rateLimited(resp *http.Response, msg string) error {
	e := &RateLimitError{Message: msg}
	if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
		if sec, err := strconv.ParseInt(reset, 10, 64); err == nil && sec > 0 {
			e.ResetAt = time.Unix(sec, 0)
		}
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if sec, err := strconv.Atoi(ra); err == nil && sec >= 0 {
			e.RetryAfter = time.Duration(sec) * time.Second
		}
	}
	return e
}

func githubMessage(body []byte) string {
	var p struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &p) == nil && p.Message != "" {
		return p.Message
	}
	return strings.TrimSpace(string(body))
}

// ---------------------------------------------------------------------------
// ID parsing
// ---------------------------------------------------------------------------

func (t *Tracker) parseID(id domain.TrackerID) (owner, repo string, number int, err error) {
	// Strict: the Session Manager picks an adapter by Provider, so reaching
	// this adapter with a non-github Provider is a routing bug, not user
	// input. Empty Provider is treated the same way — it would round-trip
	// to an Issue whose ID can't be re-routed.
	if id.Provider != domain.TrackerProviderGitHub {
		return "", "", 0, fmt.Errorf("%w: provider=%q", ErrWrongProvider, id.Provider)
	}
	return parseGitHubID(id.Native)
}

// parseGitHubID accepts "owner/repo#NUM" and returns the three components.
// Forms like "owner/repo/issues/NUM" or bare numbers are intentionally
// rejected so the rest of the system has one canonical id shape.
func parseGitHubID(native string) (owner, repo string, number int, err error) {
	hash := strings.IndexByte(native, '#')
	if hash < 0 {
		return "", "", 0, fmt.Errorf("%w: missing #issue", ErrBadID)
	}
	repoPart := native[:hash]
	numPart := native[hash+1:]
	slash := strings.IndexByte(repoPart, '/')
	if slash < 0 {
		return "", "", 0, fmt.Errorf("%w: missing owner/repo separator", ErrBadID)
	}
	owner = repoPart[:slash]
	repo = repoPart[slash+1:]
	if owner == "" || repo == "" {
		return "", "", 0, fmt.Errorf("%w: empty owner or repo", ErrBadID)
	}
	n, parseErr := strconv.Atoi(numPart)
	if parseErr != nil || n <= 0 {
		return "", "", 0, fmt.Errorf("%w: bad issue number %q", ErrBadID, numPart)
	}
	return owner, repo, n, nil
}
