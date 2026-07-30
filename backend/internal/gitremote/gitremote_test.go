package gitremote

// Tests for the credential stripping. The table below is the contract: every
// case above the divider must be stripped, every case below it must survive
// byte-identical. The ssh-versus-https asymmetry is deliberate, see sanitizeURL.
// Moved verbatim from internal/service/project/clone_test.go with the function
// (issue #41).

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "https token as username and password",
			raw:  "https://x-access-token:ghp_SECRET@github.com/acme/widgets.git",
			want: "https://github.com/acme/widgets.git",
		},
		{
			name: "https token as username only",
			raw:  "https://ghp_SECRET@github.com/acme/widgets.git",
			want: "https://github.com/acme/widgets.git",
		},
		{
			name: "https user and password",
			raw:  "https://alice:s3cr3t@git.example.com/acme/widgets.git",
			want: "https://git.example.com/acme/widgets.git",
		},
		{
			name: "https percent-encoded password",
			raw:  "https://alice:p%40ss%3Aword@git.example.com/acme/widgets.git",
			want: "https://git.example.com/acme/widgets.git",
		},
		{
			name: "http is treated the same as https",
			raw:  "http://alice:s3cr3t@git.example.com/acme/widgets.git",
			want: "http://git.example.com/acme/widgets.git",
		},
		{
			name: "port and query are preserved",
			raw:  "https://alice:s3cr3t@git.example.com:8443/acme/widgets.git",
			want: "https://git.example.com:8443/acme/widgets.git",
		},
		{
			name: "uppercase scheme",
			raw:  "HTTPS://alice:s3cr3t@git.example.com/acme/widgets.git",
			want: "https://git.example.com/acme/widgets.git",
		},

		// Everything below must survive untouched.
		{
			name: "plain https is unchanged",
			raw:  "https://github.com/acme/widgets.git",
			want: "https://github.com/acme/widgets.git",
		},
		{
			name: "scp-like shorthand keeps its ssh user",
			raw:  "git@github.com:acme/widgets.git",
			want: "git@github.com:acme/widgets.git",
		},
		{
			name: "ssh scheme keeps its login name",
			raw:  "ssh://git@github.com/acme/widgets.git",
			want: "ssh://git@github.com/acme/widgets.git",
		},
		{
			name: "ssh scheme with a port keeps its login name",
			raw:  "ssh://git@github.com:2222/acme/widgets.git",
			want: "ssh://git@github.com:2222/acme/widgets.git",
		},
		{
			name: "ssh scheme drops a password but keeps the login name",
			raw:  "ssh://git:s3cr3t@github.com/acme/widgets.git",
			want: "ssh://git@github.com/acme/widgets.git",
		},
		{
			name: "git+ssh keeps its login name",
			raw:  "git+ssh://git@github.com/acme/widgets.git",
			want: "git+ssh://git@github.com/acme/widgets.git",
		},
		{
			name: "local path is unchanged",
			raw:  "/srv/git/acme/widgets.git",
			want: "/srv/git/acme/widgets.git",
		},
		{
			name: "file url is unchanged",
			raw:  "file:///srv/git/acme/widgets.git",
			want: "file:///srv/git/acme/widgets.git",
		},
		{
			name: "empty is unchanged",
			raw:  "",
			want: "",
		},
		{
			name: "an at sign in the path is not userinfo",
			raw:  "https://git.example.com/acme/wid@gets.git",
			want: "https://git.example.com/acme/wid@gets.git",
		},
		{
			name: "unparseable input is returned verbatim",
			raw:  "git@host:acme/wid\x7fgets.git",
			want: "git@host:acme/wid\x7fgets.git",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeURL(tc.raw); got != tc.want {
				t.Fatalf("sanitizeURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSanitizeURL_LeaksNoSecret(t *testing.T) {
	const secret = "ghp_SUPERSECRET"
	for _, raw := range []string{
		"https://x-access-token:" + secret + "@github.com/acme/widgets.git",
		"https://" + secret + "@github.com/acme/widgets.git",
		"https://alice:" + secret + "@git.example.com:8443/acme/widgets.git",
		"ssh://git:" + secret + "@github.com/acme/widgets.git",
	} {
		if got := sanitizeURL(raw); strings.Contains(got, secret) {
			t.Fatalf("sanitizeURL(%q) still contains the secret: %q", raw, got)
		}
	}
}

// OriginURL must strip a credential the repo's own .git/config carries, which is
// the case for a repo added by path whose origin was set up with a token, and for
// a clone AO itself made from a credentialed cloneUrl.
func TestOriginURL_StripsCredentialFromRealRepo(t *testing.T) {
	const secret = "ghp_SUPERSECRET"
	dir := t.TempDir()
	mustGit(t, "init", dir)
	mustGit(t, "-C", dir, "remote", "add", "origin", "https://x-access-token:"+secret+"@github.com/acme/widgets.git")

	got := OriginURL(dir)
	if strings.Contains(got, secret) {
		t.Fatalf("OriginURL leaked the token: %q", got)
	}
	if got != "https://github.com/acme/widgets.git" {
		t.Fatalf("OriginURL = %q, want https://github.com/acme/widgets.git", got)
	}
}

func TestOriginURL_EmptyWhenNoOriginOrNoRepo(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, "init", dir)
	if got := OriginURL(dir); got != "" {
		t.Fatalf("OriginURL on a repo with no origin = %q, want \"\"", got)
	}
	if got := OriginURL(filepath.Join(dir, "nope")); got != "" {
		t.Fatalf("OriginURL on a missing repo = %q, want \"\"", got)
	}
	if got := OriginURL(""); got != "" {
		t.Fatalf("OriginURL(\"\") = %q, want \"\"", got)
	}
}

func mustGit(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}
