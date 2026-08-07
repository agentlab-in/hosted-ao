package vmgateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The golden cross-module contract test. Every other test in this package
// verifies tokens this package's own helpers signed and parses a JWKS this
// package's own helpers built, so none of them can notice the control plane
// drifting away from the verifier: they are separate Go modules with separate
// CI. The fixtures read here are produced by the real control plane code
// paths (keys.Manager.JWKS and tokens.Issuer.IssueAccessToken) and committed,
// which is the only way an issuer artifact can reach this module.
//
// Regenerate them in agentlab-in/ao-controlplane with:
//
//	go test ./internal/tokens/ -run TestGoldenFixtures -update
//
// then copy the fixtures into testdata here.
// See the ao-controlplane repo (internal/tokens/golden_test.go), which owns the generator,
// and TOKEN_CONTRACT.md in agentlab-in/ao-controlplane, which these fixtures make executable.

// The claims the fixtures were minted with, pinned here independently of the
// generator rather than read out of a fixture file: a token that agrees with
// itself proves nothing.
const (
	goldenIssuer    = "https://ao.agentlab.in"
	goldenAccountID = "6f1d3b0c-2a5e-4c9f-8b71-0d2e4a6c8f10"
	goldenMachineID = "b3c1a4d2-7e60-4f38-9a15-2c8d5e7f0b94"
)

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden fixture: %v\nRegenerate in agentlab-in/ao-controlplane with:\n  go test ./internal/tokens/ -run TestGoldenFixtures -update\nand copy the fixtures into this testdata directory.", err)
	}
	return data
}

func goldenKeySet(t *testing.T) *KeySet {
	t.Helper()
	ks, err := parseKeySet(readGolden(t, "jwks.json"))
	if err != nil {
		t.Fatalf("parseKeySet on the control plane's own JWKS: %v", err)
	}
	// Active key plus the next-rotation slot, per TOKEN_CONTRACT.md. A count
	// of one would mean this verifier silently skipped a published key, for
	// example over a kty, crv, or x encoding disagreement.
	if len(ks.byKID) != 2 {
		t.Fatalf("golden JWKS resolved to %d usable keys, want 2 (active plus next)", len(ks.byKID))
	}
	return ks
}

func goldenToken(t *testing.T, name string) string {
	t.Helper()
	return string(bytes.TrimSpace(readGolden(t, name)))
}

func goldenOptions() VerifyOptions {
	return VerifyOptions{
		Issuer:   goldenIssuer,
		Audience: goldenMachineID,
		Subject:  goldenAccountID,
		Skew:     DefaultSkew,
	}
}

// TestGolden_ControlPlaneTokenVerifies is the whole point: a token the control
// plane actually minted, verified against the JWKS the control plane actually
// serves, through the real verification path with a real clock.
func TestGolden_ControlPlaneTokenVerifies(t *testing.T) {
	ks := goldenKeySet(t)
	token := goldenToken(t, "access_token.jwt")

	claims, err := VerifyToken(token, ks, goldenOptions())
	if err != nil {
		t.Fatalf("VerifyToken on a real control-plane token: %v\nThe issuer and this verifier have diverged. Compare the issuer (ao-controlplane: internal/tokens/access.go, internal/keys/jwks.go) against this package, then see TOKEN_CONTRACT.md in agentlab-in/ao-controlplane.", err)
	}
	if claims.Issuer != goldenIssuer || claims.Audience != goldenMachineID || claims.Subject != goldenAccountID {
		t.Fatalf("claims = %+v, want iss %s, aud %s, sub %s", claims, goldenIssuer, goldenMachineID, goldenAccountID)
	}
	// Not a rotting fixture: exp is a century out by construction, and expiry
	// was checked for real above rather than stubbed away.
	if time.Until(claims.ExpiresAt) < 10*365*24*time.Hour {
		t.Fatalf("golden token expires at %s, too soon to keep exercising the real expiry check; regenerate the fixtures", claims.ExpiresAt)
	}
}

// TestGolden_TokenKIDResolvesInJWKS pins kid derivation, which a successful
// verification alone does not: candidates() falls back to trying every key in
// the set when a kid is unrecognised, so the token above would still verify if
// the control plane changed how it derives kid tomorrow. The hint has to
// actually hit.
func TestGolden_TokenKIDResolvesInJWKS(t *testing.T) {
	ks := goldenKeySet(t)

	parts := bytes.SplitN([]byte(goldenToken(t, "access_token.jwt")), []byte("."), 3)
	if len(parts) != 3 {
		t.Fatalf("golden token has %d parts, want 3", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(parts[0]))
	if err != nil {
		t.Fatalf("decode golden token header: %v", err)
	}
	var header jwsHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		t.Fatalf("unmarshal golden token header: %v", err)
	}
	if header.Kid == "" {
		t.Fatal("golden token carries no kid; the control plane stopped setting one")
	}
	if _, ok := ks.byKID[header.Kid]; !ok {
		t.Fatalf("golden token kid %q is not a kid in the golden JWKS: the issuer's kid derivation and the JWKS it publishes have diverged", header.Kid)
	}
}

// TestGolden_ForeignTokenRejected is the negative half: a token minted by the
// same issuer code with the same claims, signed by keys that are not in the
// published JWKS. Only the signature check can reject it.
func TestGolden_ForeignTokenRejected(t *testing.T) {
	_, err := VerifyToken(goldenToken(t, "foreign_token.jwt"), goldenKeySet(t), goldenOptions())
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("VerifyToken on a token signed by an unpublished key = %v, want ErrBadSignature", err)
	}
}

// TestGolden_WrongAudienceRejected proves the aud comparison is live against a
// real token, not just against this package's own fixtures.
func TestGolden_WrongAudienceRejected(t *testing.T) {
	opts := goldenOptions()
	opts.Audience = "some-other-machine-id"
	_, err := VerifyToken(goldenToken(t, "access_token.jwt"), goldenKeySet(t), opts)
	if !errors.Is(err, ErrAudienceMismatch) {
		t.Fatalf("VerifyToken with the wrong machine id = %v, want ErrAudienceMismatch", err)
	}
}
