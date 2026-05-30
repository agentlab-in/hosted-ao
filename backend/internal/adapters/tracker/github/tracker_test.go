package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// recordedReq captures one inbound HTTP request so tests can assert against
// the exact GitHub API surface the adapter touched.
type recordedReq struct {
	Method string
	Path   string
	Body   string
}

// fakeGH is a programmable httptest.Server that matches requests by
// "METHOD path" and records every call. Unmatched requests fail the test —
// that is the point of TDD here, so an accidental extra call is loud.
type fakeGH struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	requests []recordedReq
	handlers map[string]http.HandlerFunc
}

func newFakeGH(t *testing.T) *fakeGH {
	t.Helper()
	f := &fakeGH{t: t, handlers: map[string]http.HandlerFunc{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGH) on(method, path string, h http.HandlerFunc) {
	f.handlers[method+" "+path] = h
}

func (f *fakeGH) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, recordedReq{Method: r.Method, Path: r.URL.Path, Body: string(body)})
	f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	h, ok := f.handlers[key]
	if !ok {
		f.t.Errorf("unexpected request: %s", key)
		http.Error(w, "no handler", http.StatusNotImplemented)
		return
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	h(w, r)
}

func (f *fakeGH) calls() []recordedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedReq, len(f.requests))
	copy(out, f.requests)
	return out
}

// newTrackerForTest constructs an adapter pointed at the fake server with a
// static dev token. Production code uses EnvTokenSource; tests skip that to
// keep the surface tiny.
func newTrackerForTest(t *testing.T, f *fakeGH) *Tracker {
	t.Helper()
	tr, err := New(Options{
		BaseURL:    f.server.URL,
		Token:      StaticTokenSource("tkn-test"),
		HTTPClient: f.server.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func ctx() context.Context { return context.Background() }

func TestNewRejectsMissingToken(t *testing.T) {
	if _, err := New(Options{Token: StaticTokenSource("")}); !errors.Is(err, ErrNoToken) {
		t.Fatalf("New with empty token = %v, want ErrNoToken", err)
	}
	if _, err := New(Options{}); !errors.Is(err, ErrNoToken) {
		t.Fatalf("New with no source = %v, want ErrNoToken", err)
	}
}

func TestParseID(t *testing.T) {
	cases := []struct {
		name      string
		native    string
		wantOwner string
		wantRepo  string
		wantNum   int
		wantErr   bool
	}{
		{"happy", "octocat/hello-world#42", "octocat", "hello-world", 42, false},
		{"missing hash", "octocat/hello-world", "", "", 0, true},
		{"missing slash", "octocat#42", "", "", 0, true},
		{"empty owner", "/repo#1", "", "", 0, true},
		{"empty repo", "owner/#1", "", "", 0, true},
		{"non-numeric", "o/r#abc", "", "", 0, true},
		{"zero", "o/r#0", "", "", 0, true},
		{"negative", "o/r#-1", "", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, num, err := parseGitHubID(tc.native)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %s/%s#%d", owner, repo, num)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if owner != tc.wantOwner || repo != tc.wantRepo || num != tc.wantNum {
				t.Fatalf("got %s/%s#%d, want %s/%s#%d", owner, repo, num, tc.wantOwner, tc.wantRepo, tc.wantNum)
			}
		})
	}
}

func TestGet_HappyPath(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/repos/octocat/hello-world/issues/42", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-test" {
			t.Errorf("Authorization = %q, want Bearer tkn-test", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"number": 42,
			"title": "Found a bug",
			"body": "It does not work",
			"state": "open",
			"html_url": "https://github.com/octocat/hello-world/issues/42",
			"labels": [{"name":"bug"},{"name":"in-progress"}],
			"assignees": [{"login":"alice"},{"login":"bob"}]
		}`))
	})
	tr := newTrackerForTest(t, f)

	issue, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "octocat/hello-world#42"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := domain.Issue{
		ID:        domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "octocat/hello-world#42"},
		Title:     "Found a bug",
		Body:      "It does not work",
		State:     domain.IssueInProgress, // the "in-progress" label wins over plain "open"
		URL:       "https://github.com/octocat/hello-world/issues/42",
		Labels:    []string{"bug", "in-progress"},
		Assignees: []string{"alice", "bob"},
	}
	if !reflect.DeepEqual(issue, want) {
		t.Fatalf("issue = %#v\nwant %#v", issue, want)
	}
}

func TestGet_StateMappingFromGitHubFields(t *testing.T) {
	cases := []struct {
		name      string
		ghState   string
		ghReason  string
		labels    []string
		wantState domain.NormalizedIssueState
	}{
		{"plain open", "open", "", nil, domain.IssueOpen},
		{"open with in-progress label", "open", "", []string{"in-progress"}, domain.IssueInProgress},
		{"open with in-review label", "open", "", []string{"in-review"}, domain.IssueInReview},
		{"review wins over progress when both present", "open", "", []string{"in-progress", "in-review"}, domain.IssueInReview},
		{"closed completed", "closed", "completed", nil, domain.IssueDone},
		{"closed not_planned", "closed", "not_planned", nil, domain.IssueCancelled},
		{"closed unknown reason maps to done", "closed", "", nil, domain.IssueDone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGH(t)
			payload := map[string]any{
				"number":   1,
				"title":    "t",
				"body":     "",
				"state":    tc.ghState,
				"html_url": "https://github.com/o/r/issues/1",
			}
			if tc.ghReason != "" {
				payload["state_reason"] = tc.ghReason
			}
			if tc.labels != nil {
				ls := make([]map[string]string, len(tc.labels))
				for i, n := range tc.labels {
					ls[i] = map[string]string{"name": n}
				}
				payload["labels"] = ls
			}
			b, _ := json.Marshal(payload)
			f.on("GET", "/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(b)
			})
			tr := newTrackerForTest(t, f)
			issue, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"})
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if issue.State != tc.wantState {
				t.Fatalf("state = %q, want %q", issue.State, tc.wantState)
			}
		})
	}
}

func TestGet_NotFound(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGet_RateLimited(t *testing.T) {
	f := newFakeGH(t)
	reset := time.Now().Add(2 * time.Minute).Unix()
	f.on("GET", "/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if got := rle.ResetAt.Unix(); got != reset {
		t.Fatalf("ResetAt = %d, want %d", got, reset)
	}
}

// TestGet_SecondaryRateLimit covers the GitHub "abuse detection"
// response — it lacks X-RateLimit-Remaining but sets Retry-After, and the
// body mentions the limit. The classifier must still surface this as
// ErrRateLimited rather than mis-categorizing it as auth failure.
func TestGet_SecondaryRateLimit(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, `{"message":"You have exceeded a secondary rate limit"}`, http.StatusForbidden)
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if rle.RetryAfter != 60*time.Second {
		t.Fatalf("RetryAfter = %v, want 60s", rle.RetryAfter)
	}
}

func TestGet_RejectsWrongProvider(t *testing.T) {
	f := newFakeGH(t)
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "g/p#1"})
	if !errors.Is(err, ErrWrongProvider) {
		t.Fatalf("err = %v, want ErrWrongProvider", err)
	}
}

func TestGet_RejectsEmptyProvider(t *testing.T) {
	f := newFakeGH(t)
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Native: "o/r#1"})
	if !errors.Is(err, ErrWrongProvider) {
		t.Fatalf("err = %v, want ErrWrongProvider", err)
	}
}

// TestGet_CanonicalizesProviderOnOutput pins the contract that returned
// Issues always carry domain.TrackerProviderGitHub, so callers can re-route
// without inspecting which adapter they originally talked to.
func TestGet_CanonicalizesProviderOnOutput(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"number":1,"title":"t","body":"","state":"open","html_url":"https://github.com/o/r/issues/1"}`))
	})
	tr := newTrackerForTest(t, f)
	issue, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if issue.ID.Provider != domain.TrackerProviderGitHub {
		t.Fatalf("issue.ID.Provider = %q, want %q", issue.ID.Provider, domain.TrackerProviderGitHub)
	}
	if issue.ID.Native != "o/r#1" {
		t.Fatalf("issue.ID.Native = %q, want o/r#1", issue.ID.Native)
	}
}

func TestComment_HappyPath(t *testing.T) {
	f := newFakeGH(t)
	f.on("POST", "/repos/o/r/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Body != "hello world" {
			t.Errorf("body = %q, want %q", got.Body, "hello world")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	})
	tr := newTrackerForTest(t, f)
	if err := tr.Comment(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"}, "hello world"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
}

// TestComment_PreservesMarkdownBody locks in that we POST the body verbatim
// — no trimming, no escape-and-unescape round trip — so multi-line markdown
// notifications from the SM survive.
func TestComment_PreservesMarkdownBody(t *testing.T) {
	f := newFakeGH(t)
	body := "## status\n\n- step 1: done\n- step 2: **in progress**\n\n```go\nfmt.Println(\"hi\")\n```\n"
	f.on("POST", "/repos/o/r/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Body != body {
			t.Errorf("body = %q, want %q", got.Body, body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	})
	tr := newTrackerForTest(t, f)
	if err := tr.Comment(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"}, body); err != nil {
		t.Fatalf("Comment: %v", err)
	}
}

func TestComment_RejectsEmptyBody(t *testing.T) {
	f := newFakeGH(t)
	tr := newTrackerForTest(t, f)
	for _, body := range []string{"", "   ", "\n\t"} {
		err := tr.Comment(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"}, body)
		if !errors.Is(err, ErrEmptyBody) {
			t.Fatalf("body %q: err = %v, want ErrEmptyBody", body, err)
		}
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Fatalf("unexpected calls on empty body: %#v", calls)
	}
}

// transitionCall is the normalized record of one GH API call made by
// Transition. The tests compare a sorted slice of these against the expected
// call set so we don't depend on call ordering.
type transitionCall struct {
	Method string
	Path   string
	// for PATCH /issues/N — JSON keys we care about
	State       string
	StateReason string
	// for POST .../labels — labels added
	AddLabels []string
}

func TestTransition_MapsToCorrectGitHubCalls(t *testing.T) {
	cases := []struct {
		name  string
		state domain.NormalizedIssueState
		want  []transitionCall
	}{
		{
			name:  "open clears status labels and reopens",
			state: domain.IssueOpen,
			want: []transitionCall{
				{Method: "PATCH", Path: "/repos/o/r/issues/1", State: "open"},
				{Method: "DELETE", Path: "/repos/o/r/issues/1/labels/in-progress"},
				{Method: "DELETE", Path: "/repos/o/r/issues/1/labels/in-review"},
			},
		},
		{
			name:  "in_progress adds in-progress label, removes in-review",
			state: domain.IssueInProgress,
			want: []transitionCall{
				{Method: "PATCH", Path: "/repos/o/r/issues/1", State: "open"},
				{Method: "POST", Path: "/repos/o/r/issues/1/labels", AddLabels: []string{"in-progress"}},
				{Method: "DELETE", Path: "/repos/o/r/issues/1/labels/in-review"},
			},
		},
		{
			name:  "review adds in-review label, removes in-progress",
			state: domain.IssueInReview,
			want: []transitionCall{
				{Method: "PATCH", Path: "/repos/o/r/issues/1", State: "open"},
				{Method: "POST", Path: "/repos/o/r/issues/1/labels", AddLabels: []string{"in-review"}},
				{Method: "DELETE", Path: "/repos/o/r/issues/1/labels/in-progress"},
			},
		},
		{
			name:  "done closes as completed and cleans status labels",
			state: domain.IssueDone,
			want: []transitionCall{
				{Method: "PATCH", Path: "/repos/o/r/issues/1", State: "closed", StateReason: "completed"},
				{Method: "DELETE", Path: "/repos/o/r/issues/1/labels/in-progress"},
				{Method: "DELETE", Path: "/repos/o/r/issues/1/labels/in-review"},
			},
		},
		{
			name:  "cancelled closes as not_planned and cleans status labels",
			state: domain.IssueCancelled,
			want: []transitionCall{
				{Method: "PATCH", Path: "/repos/o/r/issues/1", State: "closed", StateReason: "not_planned"},
				{Method: "DELETE", Path: "/repos/o/r/issues/1/labels/in-progress"},
				{Method: "DELETE", Path: "/repos/o/r/issues/1/labels/in-review"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGH(t)
			// PATCH endpoint returns an updated issue body
			f.on("PATCH", "/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"number":1,"state":"open"}`))
			})
			// label-add endpoint
			f.on("POST", "/repos/o/r/issues/1/labels", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`[]`))
			})
			// label-remove endpoints — return 404 sometimes to confirm we ignore it
			f.on("DELETE", "/repos/o/r/issues/1/labels/in-progress", func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"message":"Label does not exist"}`, http.StatusNotFound)
			})
			f.on("DELETE", "/repos/o/r/issues/1/labels/in-review", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`[]`))
			})
			tr := newTrackerForTest(t, f)
			if err := tr.Transition(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"}, tc.state); err != nil {
				t.Fatalf("Transition: %v", err)
			}
			got := normalizeCalls(t, f.calls())
			want := append([]transitionCall(nil), tc.want...)
			sortCalls(got)
			sortCalls(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("calls:\n got  %#v\n want %#v", got, want)
			}
		})
	}
}

func TestTransition_RejectsUnknownState(t *testing.T) {
	f := newFakeGH(t)
	tr := newTrackerForTest(t, f)
	err := tr.Transition(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"}, domain.NormalizedIssueState("frobnicated"))
	if !errors.Is(err, ErrUnknownState) {
		t.Fatalf("err = %v, want ErrUnknownState", err)
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Fatalf("unexpected calls: %#v", calls)
	}
}

// normalizeCalls converts the recordedReq slice into transitionCall records
// the test cases assert against, decoding the PATCH/label-add bodies.
func normalizeCalls(t *testing.T, reqs []recordedReq) []transitionCall {
	t.Helper()
	out := make([]transitionCall, 0, len(reqs))
	for _, r := range reqs {
		tc := transitionCall{Method: r.Method, Path: r.Path}
		switch {
		case r.Method == "PATCH":
			var body struct {
				State       string `json:"state"`
				StateReason string `json:"state_reason"`
			}
			if r.Body != "" {
				if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
					t.Fatalf("patch body: %v", err)
				}
			}
			tc.State = body.State
			tc.StateReason = body.StateReason
		case r.Method == "POST" && strings.HasSuffix(r.Path, "/labels"):
			var body struct {
				Labels []string `json:"labels"`
			}
			if r.Body != "" {
				if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
					t.Fatalf("labels body: %v", err)
				}
			}
			tc.AddLabels = body.Labels
		}
		out = append(out, tc)
	}
	return out
}

func sortCalls(s []transitionCall) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Method != s[j].Method {
			return s[i].Method < s[j].Method
		}
		return s[i].Path < s[j].Path
	})
}
