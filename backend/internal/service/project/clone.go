package project

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// cloneTimeout bounds how long `git clone` may run. It is generous and
// deliberately decoupled from the daemon's per-request HTTP timeout (see
// cloneRepository) because project.Add clones synchronously — no job model
// with progress reporting exists yet.
//
// A var rather than a const only so the timeout rejection path is testable
// without a five minute test.
var cloneTimeout = 5 * time.Minute

// cloneWaitDelay bounds how long CombinedOutput may keep waiting on the output
// pipes after the clone deadline has killed `git`. Without it CLONE_TIMEOUT is
// unreachable in practice: `git clone` delegates to a `git-remote-http` (or
// `ssh`) child that inherits the pipe, killing the parent does not kill the
// child, and a child blocked on a stalled server holds the pipe open for as
// long as it likes. cloneRepository would then never return, and because it
// runs under Add's addMu, every later project add would block behind it.
//
// Also a var only so the test does not have to wait it out.
var cloneWaitDelay = 5 * time.Second

// cloneRepository clones cloneURL into <reposRoot>/<owner>-<repo> and returns
// the destination path. Every failure is mapped to a remediation-shaped
// *apierr.Error; the raw git error text never reaches the caller.
func (m *Service) cloneRepository(ctx context.Context, cloneURL string) (string, error) {
	if strings.TrimSpace(m.reposRoot) == "" {
		return "", apierr.Internal("CLONE_NOT_CONFIGURED", "Cloning by URL is not configured on this daemon")
	}
	owner, repo, err := parseCloneURL(cloneURL)
	if err != nil {
		return "", apierr.Invalid("CLONE_URL_INVALID", "Could not parse the git URL. Use an https:// or ssh git remote URL.", nil)
	}
	if err := os.MkdirAll(m.reposRoot, 0o750); err != nil {
		return "", apierr.Internal("CLONE_DEST_UNAVAILABLE", "Could not prepare the clone destination directory")
	}
	dest := filepath.Join(m.reposRoot, owner+"-"+repo)
	if _, err := os.Stat(dest); err == nil {
		return "", apierr.Conflict("CLONE_DESTINATION_EXISTS",
			fmt.Sprintf("A repository is already cloned at %s. Remove it or register the existing clone by path instead.", dest),
			map[string]any{"path": dest})
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", apierr.Internal("CLONE_DEST_UNAVAILABLE", "Could not inspect the clone destination directory")
	}

	// Decoupled from the incoming request's own deadline (the daemon's REST
	// group applies a short per-request timeout, see httpd/api.go): only this
	// clone's own generous timeout should bound it.
	cloneCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cloneTimeout)
	defer cancel()

	cmd := aoprocess.CommandContext(cloneCtx, "git", "clone", "--", cloneURL, dest)
	cmd.WaitDelay = cloneWaitDelay
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", // never block on an interactive credential prompt
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=15", // never block on an SSH password/host-key prompt
	)
	out, cloneErr := cmd.CombinedOutput()
	if cloneErr == nil {
		return dest, nil
	}
	_ = os.RemoveAll(dest) // don't leave a partial clone blocking a retry under the same name

	if cloneCtx.Err() != nil {
		return "", apierr.Invalid("CLONE_TIMEOUT",
			fmt.Sprintf("Clone timed out after %s. Check your network connection and try again.", cloneTimeout), nil)
	}
	return "", classifyCloneError(string(out))
}

// classifyCloneError maps common git clone failure text to actionable,
// remediation-shaped errors. The raw git output is intentionally not included
// in the returned error: callers must never see it.
func classifyCloneError(output string) *apierr.Error {
	lower := strings.ToLower(output)
	switch {
	case containsAny(lower, "could not read username", "could not read password", "authentication failed", "permission denied (publickey)", "terminal prompts disabled", "invalid credentials"):
		return apierr.Invalid("CLONE_AUTH_FAILED",
			"No git credentials on this machine. For an https:// URL, run `gh auth login`. For an SSH URL, add a deploy key or start an SSH agent, then try again.",
			nil)
	case containsAny(lower, "could not resolve host", "temporary failure in name resolution"):
		return apierr.Invalid("CLONE_HOST_NOT_FOUND",
			"Could not reach the git host. Check the URL and your network connection.",
			nil)
	case containsAny(lower, "host key verification failed"):
		return apierr.Invalid("CLONE_HOST_KEY_UNVERIFIED",
			"The SSH host key for this git host is not recognized on this machine. Run `ssh -T <user>@<host>` once to accept it, or use an https:// URL instead.",
			nil)
	case containsAny(lower, "repository not found", "not found"):
		return apierr.Invalid("CLONE_REPO_NOT_FOUND",
			"Repository not found. Check the URL and that you have access to it.",
			nil)
	default:
		return apierr.Invalid("CLONE_FAILED", "Could not clone the repository. Check the URL and try again.", nil)
	}
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// scpLikeCloneURL matches the scp-like git remote syntax, e.g.
// "git@github.com:owner/repo.git" — a form net/url.Parse does not recognise
// as having a scheme/host.
var scpLikeCloneURL = regexp.MustCompile(`^[\w.-]+@[\w.-]+:(.+)$`)

// safeDirComponent restricts a parsed owner/repo segment to characters safe
// for a single path component, rejecting anything that could traverse or
// otherwise misbehave once joined into reposRoot.
var safeDirComponent = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// parseCloneURL extracts the owner and repository name from a git remote URL,
// used to build the <owner>-<repo> clone destination name. It accepts
// scheme-qualified URLs (https://, ssh://, git://, file://) and the scp-like
// SSH shorthand (user@host:owner/repo.git).
func parseCloneURL(raw string) (owner, repo string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("clone url is required")
	}

	var pathPart string
	if u, uerr := url.Parse(raw); uerr == nil && u.Scheme != "" {
		pathPart = u.Path
	} else if m := scpLikeCloneURL.FindStringSubmatch(raw); m != nil {
		pathPart = m[1]
	} else {
		return "", "", fmt.Errorf("unrecognized clone url %q", raw)
	}

	pathPart = strings.Trim(pathPart, "/")
	pathPart = strings.TrimSuffix(pathPart, ".git")
	segments := strings.Split(pathPart, "/")
	if len(segments) < 2 {
		return "", "", fmt.Errorf("clone url %q must include an owner and a repository", raw)
	}

	repo, err = sanitizeDirComponent(segments[len(segments)-1])
	if err != nil {
		return "", "", err
	}
	owner, err = sanitizeDirComponent(segments[len(segments)-2])
	if err != nil {
		return "", "", err
	}
	return owner, repo, nil
}

func sanitizeDirComponent(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." || !safeDirComponent.MatchString(s) {
		return "", fmt.Errorf("invalid path component %q", s)
	}
	return s, nil
}
