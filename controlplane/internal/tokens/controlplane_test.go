package tokens

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/keys"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/storage/sqlite"
)

func TestIssueControlPlaneToken_ClaimsAndAudience(t *testing.T) {
	issuer, km, _ := newTestIssuer(t)

	token, err := issuer.IssueControlPlaneToken(testAccountID)
	if err != nil {
		t.Fatalf("IssueControlPlaneToken() unexpected error: %v", err)
	}
	header, claims, _, _ := decodeJWT(t, token)

	wantKID, _ := km.Active()
	if header.Alg != "EdDSA" || header.Kid != wantKID {
		t.Errorf("header = %+v, want EdDSA signed by the active kid %q", header, wantKID)
	}
	if claims.Iss != "https://ao.agentlab.in" {
		t.Errorf("iss = %q, want the control plane origin", claims.Iss)
	}
	// The audience is the control plane itself: this token says "call the
	// control plane API", not "call a machine gateway".
	if claims.Aud != claims.Iss {
		t.Errorf("aud = %q, want the control plane origin %q", claims.Aud, claims.Iss)
	}
	if claims.Sub != testAccountID {
		t.Errorf("sub = %q, want %q", claims.Sub, testAccountID)
	}
	if claims.Jti == "" || claims.Iat == 0 || claims.Exp == 0 {
		t.Errorf("claims = %+v, want jti, iat, and exp all set", claims)
	}
}

func TestVerifyControlPlaneToken_RejectsEverythingNotAddressedHere(t *testing.T) {
	issuer, _, _ := newTestIssuer(t)

	valid, err := issuer.IssueControlPlaneToken(testAccountID)
	if err != nil {
		t.Fatalf("IssueControlPlaneToken() unexpected error: %v", err)
	}
	if got, err := issuer.VerifyControlPlaneToken(valid); err != nil || got != testAccountID {
		t.Fatalf("VerifyControlPlaneToken(valid) = %q, %v, want %q, nil", got, err, testAccountID)
	}

	// A machine-audience token is signed by the same key and issued by the
	// same service. Only the audience separates it, which is the whole point.
	machineToken, err := issuer.IssueAccessToken(testAccountID, "machine-1")
	if err != nil {
		t.Fatalf("IssueAccessToken() unexpected error: %v", err)
	}
	if _, err := issuer.VerifyControlPlaneToken(machineToken); err != ErrTokenClaims {
		t.Errorf("machine-audience token: err = %v, want ErrTokenClaims", err)
	}

	// Another control plane's token: same shape and claims, different key.
	otherKM, err := keys.Load(t.TempDir())
	if err != nil {
		t.Fatalf("keys.Load() unexpected error: %v", err)
	}
	otherDB, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sqlite.Open() unexpected error: %v", err)
	}
	defer otherDB.Close()
	foreign, err := NewIssuer(otherKM, otherDB, "https://ao.agentlab.in", 15*time.Minute).IssueControlPlaneToken(testAccountID)
	if err != nil {
		t.Fatalf("IssueControlPlaneToken() on the other issuer: %v", err)
	}
	if _, err := issuer.VerifyControlPlaneToken(foreign); err != ErrTokenMalformed {
		// An unknown kid never resolves to a key, so it cannot even reach the
		// signature check.
		t.Errorf("another control plane's token: err = %v, want ErrTokenMalformed", err)
	}

	// A token from an issuer that shares this one's keys but claims a
	// different origin.
	sameKeysOtherOrigin, err := NewIssuer(mustKeys(t, issuer), otherDB, "https://evil.example", 15*time.Minute).IssueControlPlaneToken(testAccountID)
	if err != nil {
		t.Fatalf("IssueControlPlaneToken() on the relabelled issuer: %v", err)
	}
	if _, err := issuer.VerifyControlPlaneToken(sameKeysOtherOrigin); err != ErrTokenClaims {
		t.Errorf("token claiming another issuer: err = %v, want ErrTokenClaims", err)
	}

	// Expired, including the "no exp at all" case, which is a rejection
	// rather than "no expiry".
	expired, err := NewIssuer(mustKeys(t, issuer), otherDB, "https://ao.agentlab.in", time.Nanosecond).IssueControlPlaneToken(testAccountID)
	if err != nil {
		t.Fatalf("IssueControlPlaneToken() on the short-lived issuer: %v", err)
	}
	if _, err := issuer.VerifyControlPlaneToken(expired); err != ErrTokenExpired {
		t.Errorf("expired token: err = %v, want ErrTokenExpired", err)
	}
	if _, err := issuer.VerifyControlPlaneToken(reSign(t, issuer, valid, func(c *accessClaims) { c.Exp = 0 })); err != ErrTokenExpired {
		t.Error("a token with no exp was accepted, want it rejected")
	}

	// A token whose payload was edited keeps its old signature.
	tampered := tamperClaims(t, valid)
	if _, err := issuer.VerifyControlPlaneToken(tampered); err != ErrTokenSignature {
		t.Errorf("tampered claims: err = %v, want ErrTokenSignature", err)
	}

	// An empty sub authorizes nobody.
	if _, err := issuer.VerifyControlPlaneToken(reSign(t, issuer, valid, func(c *accessClaims) { c.Sub = "" })); err != ErrTokenClaims {
		t.Error("a token with no sub was accepted, want it rejected")
	}

	// Structurally broken input, and the alg=none classic.
	for name, token := range map[string]string{
		"empty":         "",
		"one part":      "abc",
		"two parts":     "abc.def",
		"four parts":    valid + ".extra",
		"not base64":    "!!!.???.###",
		"alg none":      unsignedToken(t, valid),
		"missing sig":   strings.TrimSuffix(valid[:strings.LastIndex(valid, ".")+1], ""),
		"bearer prefix": "Bearer " + valid,
	} {
		if _, err := issuer.VerifyControlPlaneToken(token); err == nil {
			t.Errorf("%s: token was accepted, want an error", name)
		}
	}
}

// mustKeys returns the issuer's key manager, so a test can build a second
// issuer that signs with the same key but differs in issuer or TTL.
func mustKeys(t *testing.T, i *Issuer) *keys.Manager {
	t.Helper()
	if i.keys == nil {
		t.Fatal("issuer has no key manager")
	}
	return i.keys
}

// reSign rebuilds a token with edited claims and a valid signature, so a test
// can isolate a claim check from the signature check.
func reSign(t *testing.T, i *Issuer, token string, edit func(*accessClaims)) string {
	t.Helper()

	_, claims, _, _ := decodeJWT(t, token)
	edit(&claims)
	kid, priv := i.keys.Active()
	signed, err := signEdDSA(kid, priv, claims)
	if err != nil {
		t.Fatalf("re-sign: %v", err)
	}
	return signed
}

// tamperClaims edits the payload of a signed token without re-signing it.
func tamperClaims(t *testing.T, token string) string {
	t.Helper()

	parts := strings.Split(token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims accessClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	claims.Sub = "somebody-else"
	edited, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(edited)
	return strings.Join(parts, ".")
}

// unsignedToken rewrites a token's header to alg=none and drops the
// signature, the oldest JWT attack there is.
func unsignedToken(t *testing.T, token string) string {
	t.Helper()

	parts := strings.Split(token, ".")
	header, err := json.Marshal(jwtHeader{Alg: "none", Typ: "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + parts[1] + "."
}
