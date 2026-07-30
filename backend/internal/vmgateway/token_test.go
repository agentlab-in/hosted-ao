package vmgateway

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

const (
	testIssuer      = "https://ao.agentlab.in"
	testAud         = "machine-1"
	testSub         = "account-1"
	testKid         = "key-1"
	testFallbackKid = "key-2"
)

func b64(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// signToken builds a compact JWS from arbitrary header/claims maps, signed
// with priv when alg is EdDSA-shaped; a caller testing alg-confusion passes a
// header whose alg is something else and an empty/garbage signature, since a
// non-EdDSA alg must be rejected before any signature is even considered.
func signToken(t *testing.T, priv ed25519.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	headerB64 := b64(header)
	payloadB64 := b64(claims)
	signingInput := headerB64 + "." + payloadB64
	sig := ed25519.Sign(priv, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func testKeySet(t *testing.T, kid string, pub ed25519.PublicKey) *KeySet {
	t.Helper()
	return &KeySet{byKID: map[string]ed25519.PublicKey{kid: pub}}
}

func validClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss": testIssuer,
		"sub": testSub,
		"aud": testAud,
		"exp": now.Add(15 * time.Minute).Unix(),
	}
}

func validOpts(now time.Time) VerifyOptions {
	return VerifyOptions{
		Issuer:   testIssuer,
		Audience: testAud,
		Subject:  testSub,
		Skew:     DefaultSkew,
		Now:      func() time.Time { return now },
	}
}

func TestVerifyToken_Valid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	token := signToken(t, priv, map[string]any{"alg": "EdDSA", "kid": testKid}, validClaims(now))

	claims, err := VerifyToken(token, testKeySet(t, testKid, pub), validOpts(now))
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if claims.Issuer != testIssuer || claims.Subject != testSub || claims.Audience != testAud {
		t.Errorf("unexpected claims: %+v", claims)
	}
}

func TestVerifyToken_MalformedShape(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	_, err := VerifyToken("not.a.valid.jwt.shape", testKeySet(t, testKid, pub), validOpts(time.Now()))
	if !errors.Is(err, ErrMalformedToken) {
		t.Fatalf("err = %v, want ErrMalformedToken", err)
	}
}

func TestVerifyToken_RejectsAlgNone(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	// "none" tokens conventionally carry an empty signature segment; use one
	// verbatim so this test fails if alg checking is ever bypassed for an
	// empty/absent signature.
	headerB64 := b64(map[string]any{"alg": "none"})
	payloadB64 := b64(validClaims(now))
	token := headerB64 + "." + payloadB64 + "."

	_, err := VerifyToken(token, testKeySet(t, testKid, pub), validOpts(now))
	if !errors.Is(err, ErrUnsupportedAlg) {
		t.Fatalf("err = %v, want ErrUnsupportedAlg", err)
	}
}

func TestVerifyToken_RejectsHS256AlgConfusion(t *testing.T) {
	// Simulates an attacker who HMAC-signs a token using the Ed25519 public
	// key (published at /.well-known/jwks.json) as the HMAC secret, hoping a
	// verifier that dispatches on the token's own `alg` will use it as an
	// HMAC key. VerifyToken must reject on alg alone, never reaching a key.
	pub, _, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	headerB64 := b64(map[string]any{"alg": "HS256"})
	payloadB64 := b64(validClaims(now))
	token := headerB64 + "." + payloadB64 + "." + base64.RawURLEncoding.EncodeToString([]byte("forged"))

	_, err := VerifyToken(token, testKeySet(t, testKid, pub), validOpts(now))
	if !errors.Is(err, ErrUnsupportedAlg) {
		t.Fatalf("err = %v, want ErrUnsupportedAlg", err)
	}
}

func TestVerifyToken_BadSignature(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	otherPub, _, _ := ed25519.GenerateKey(nil) // different keypair than the signer
	now := time.Now()
	token := signToken(t, priv, map[string]any{"alg": "EdDSA", "kid": testKid}, validClaims(now))

	_, err := VerifyToken(token, testKeySet(t, testKid, otherPub), validOpts(now))
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
}

func TestVerifyToken_TamperedPayload(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	token := signToken(t, priv, map[string]any{"alg": "EdDSA", "kid": testKid}, validClaims(now))

	// Swap the payload segment for one claiming a different (still
	// well-formed) subject, keeping the original signature.
	forged := map[string]any{"iss": testIssuer, "sub": "attacker-account", "aud": testAud, "exp": now.Add(time.Hour).Unix()}
	parts := splitJWT(t, token)
	tampered := parts[0] + "." + b64(forged) + "." + parts[2]

	_, err := VerifyToken(tampered, testKeySet(t, testKid, pub), validOpts(now))
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
}

func TestVerifyToken_ExpiredBeyondSkew(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	claims := map[string]any{"iss": testIssuer, "sub": testSub, "aud": testAud, "exp": now.Add(-2 * time.Minute).Unix()}
	token := signToken(t, priv, map[string]any{"alg": "EdDSA", "kid": testKid}, claims)

	_, err := VerifyToken(token, testKeySet(t, testKid, pub), validOpts(now))
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("err = %v, want ErrTokenExpired", err)
	}
}

func TestVerifyToken_ExpiredWithinSkewTolerance(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	claims := map[string]any{"iss": testIssuer, "sub": testSub, "aud": testAud, "exp": now.Add(-30 * time.Second).Unix()}
	token := signToken(t, priv, map[string]any{"alg": "EdDSA", "kid": testKid}, claims)

	if _, err := VerifyToken(token, testKeySet(t, testKid, pub), validOpts(now)); err != nil {
		t.Fatalf("VerifyToken: %v, want token within 60s skew to be accepted", err)
	}
}

func TestVerifyToken_IssuerMismatch(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	claims := validClaims(now)
	claims["iss"] = "https://evil.example.com"
	token := signToken(t, priv, map[string]any{"alg": "EdDSA", "kid": testKid}, claims)

	_, err := VerifyToken(token, testKeySet(t, testKid, pub), validOpts(now))
	if !errors.Is(err, ErrIssuerMismatch) {
		t.Fatalf("err = %v, want ErrIssuerMismatch", err)
	}
}

func TestVerifyToken_AudienceMismatch(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	claims := validClaims(now)
	claims["aud"] = "some-other-machine"
	token := signToken(t, priv, map[string]any{"alg": "EdDSA", "kid": testKid}, claims)

	_, err := VerifyToken(token, testKeySet(t, testKid, pub), validOpts(now))
	if !errors.Is(err, ErrAudienceMismatch) {
		t.Fatalf("err = %v, want ErrAudienceMismatch", err)
	}
}

func TestVerifyToken_SubjectNotAllowlisted(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	claims := validClaims(now)
	claims["sub"] = "some-other-account"
	token := signToken(t, priv, map[string]any{"alg": "EdDSA", "kid": testKid}, claims)

	_, err := VerifyToken(token, testKeySet(t, testKid, pub), validOpts(now))
	if !errors.Is(err, ErrSubjectMismatch) {
		t.Fatalf("err = %v, want ErrSubjectMismatch", err)
	}
}

func TestVerifyToken_MissingExp(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	claims := map[string]any{"iss": testIssuer, "sub": testSub, "aud": testAud}
	token := signToken(t, priv, map[string]any{"alg": "EdDSA", "kid": testKid}, claims)

	_, err := VerifyToken(token, testKeySet(t, testKid, pub), validOpts(now))
	if !errors.Is(err, ErrMalformedToken) {
		t.Fatalf("err = %v, want ErrMalformedToken", err)
	}
}

func TestVerifyToken_UnknownKidFallsBackToOtherKeys(t *testing.T) {
	// Simulates rotation: the token was signed with the active key, but its
	// kid is not (yet) the one this cache happens to look up first — the
	// verifier must still find it among the set's other keys.
	pub, priv, _ := ed25519.GenerateKey(nil)
	otherPub, _, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	token := signToken(t, priv, map[string]any{"alg": "EdDSA", "kid": "unrecognised-kid"}, validClaims(now))

	ks := &KeySet{byKID: map[string]ed25519.PublicKey{
		testFallbackKid: otherPub,
		testKid:         pub,
	}}
	if _, err := VerifyToken(token, ks, validOpts(now)); err != nil {
		t.Fatalf("VerifyToken: %v, want fallback-to-other-keys to succeed", err)
	}
}

func TestVerifyToken_EmptyKeySetFailsClosed(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	token := signToken(t, priv, map[string]any{"alg": "EdDSA", "kid": testKid}, validClaims(now))

	_, err := VerifyToken(token, &KeySet{byKID: map[string]ed25519.PublicKey{}}, validOpts(now))
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature (fail closed on empty keyset)", err)
	}

	_, err = VerifyToken(token, nil, validOpts(now))
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature (fail closed on nil keyset)", err)
	}
}

func splitJWT(t *testing.T, token string) [3]string {
	t.Helper()
	var out [3]string
	start := 0
	seg := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			out[seg] = token[start:i]
			seg++
			start = i + 1
		}
	}
	out[seg] = token[start:]
	if seg != 2 {
		t.Fatalf("token %q does not have 3 segments", token)
	}
	return out
}
