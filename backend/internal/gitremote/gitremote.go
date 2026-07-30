// Package gitremote reads a git repository's `origin` remote URL. It exists so
// that credential stripping cannot be bypassed: OriginURL is the only accessor,
// and it always sanitizes, so no caller has to remember to. Three independently
// written resolvers previously duplicated this read and two of them persisted
// the raw URL (issue #41).
package gitremote

import (
	"net/url"
	"strings"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// OriginURL returns path's `origin` remote URL via
// `git -C path remote get-url origin`, with any embedded credential stripped.
// A missing remote, missing repo, or any other git error returns an empty
// string: `project add` must not fail just because no origin is configured (the
// SCM observer skips such projects).
//
// The result is sanitized because everything downstream of this function is a
// place a credential must never reach: it is persisted to
// projects.repo_origin_url, served as Project.Repo, and logged. That covers
// both a credentialed AddInput.CloneURL (git records it verbatim as origin) and
// a repo added by path whose own origin already carries one.
func OriginURL(path string) string {
	if path == "" {
		return ""
	}
	out, err := aoprocess.Command("git", "-C", path, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return sanitizeURL(strings.TrimSpace(string(out)))
}

// sanitizeURL removes any credential embedded in a git remote URL. A client may
// legitimately POST `cloneUrl` as
// `https://x-access-token:<token>@github.com/owner/repo.git`, which is how
// non-interactive https git auth is normally expressed and what
// GIT_TERMINAL_PROMPT=0 leaves as the practical option; git then records that
// string verbatim as the repo's `origin`. The token has a job to do there, so
// the clone's own .git/config keeps it, but it must not travel any further.
//
// raw is returned unchanged when there is nothing to strip, when it does not
// parse as a URL at all, and for the scp-like SSH shorthand
// (git@host:owner/repo.git), which url.Parse rejects outright and whose
// userinfo is an SSH login name rather than a secret.
func sanitizeURL(raw string) string {
	if !strings.Contains(raw, "@") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.User == nil {
		return raw
	}
	switch strings.ToLower(u.Scheme) {
	case "ssh", "git+ssh":
		// An ssh:// URL's userinfo is the login name (`git`), which is not a
		// secret and which the remote stops working without. Drop only a
		// password, which has no legitimate use over SSH.
		if _, hasPassword := u.User.Password(); !hasPassword {
			return raw
		}
		u.User = url.User(u.User.Username())
	default:
		// Over http(s) the username alone is a credential too: a bare
		// `https://<token>@github.com/...` authenticates with no password.
		u.User = nil
	}
	return u.String()
}
