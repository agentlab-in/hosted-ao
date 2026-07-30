package vmgateway

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultSkew is the exp tolerance TOKEN_CONTRACT.md specifies.
const DefaultSkew = 60 * time.Second

// Claims is the subset of the AO access token's payload ao-vm-serve checks,
// per TOKEN_CONTRACT.md.
type Claims struct {
	Issuer    string
	Subject   string
	Audience  string
	ExpiresAt time.Time
}

// VerifyOptions pins the expected iss/aud/sub for one machine, per
// TOKEN_CONTRACT.md's "Verification on the VM" line: signature against
// cached JWKS, iss, aud equal to this machine's id, exp with 60s skew
// tolerance, and sub equal to the single account id in the machine's
// allowlist.
type VerifyOptions struct {
	Issuer   string           // expected `iss`
	Audience string           // expected `aud`: this machine's id
	Subject  string           // expected `sub`: this machine's single allowlisted account id
	Skew     time.Duration    // exp tolerance; use DefaultSkew
	Now      func() time.Time // defaults to time.Now
}

// Sentinel errors so callers (the auth middleware) can log a category without
// ever needing the raw token or its claims to explain a rejection.
var (
	ErrMalformedToken   = errors.New("malformed token")
	ErrUnsupportedAlg   = errors.New("unsupported or missing alg")
	ErrBadSignature     = errors.New("signature verification failed")
	ErrTokenExpired     = errors.New("token expired")
	ErrIssuerMismatch   = errors.New("issuer mismatch")
	ErrAudienceMismatch = errors.New("audience mismatch")
	ErrSubjectMismatch  = errors.New("subject not allowlisted")
)

type jwsHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type jwsClaims struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
}

// VerifyToken verifies a compact JWS access token against ks and opts: the
// signature (EdDSA only), `iss`, `aud`, `sub`, and `exp` with opts.Skew
// tolerance. It never logs or returns the raw token, and it never trusts a
// claim before the signature covering it has been checked.
func VerifyToken(token string, ks *KeySet, opts VerifyOptions) (Claims, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	if opts.Issuer == "" || opts.Audience == "" || opts.Subject == "" {
		return Claims{}, errors.New("verify options: issuer, audience, and subject are all required")
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("%w: expected 3 dot-separated parts, got %d", ErrMalformedToken, len(parts))
	}
	headerB64, payloadB64, sigB64 := parts[0], parts[1], parts[2]

	headerRaw, err := base64.RawURLEncoding.DecodeString(headerB64)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: header: %w", ErrMalformedToken, err)
	}
	var header jwsHeader
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		return Claims{}, fmt.Errorf("%w: header: %w", ErrMalformedToken, err)
	}
	// EdDSA only. Rejecting any other `alg` here, before a key is even
	// consulted, is what defeats algorithm-confusion attacks: "none" (a token
	// that claims to need no signature at all) and HMAC variants (which would
	// let an attacker sign with the Ed25519 public key, published at
	// /.well-known/jwks.json, misused as an HMAC secret) never reach
	// ed25519.Verify.
	if header.Alg != "EdDSA" {
		return Claims{}, ErrUnsupportedAlg
	}

	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return Claims{}, ErrBadSignature
	}
	if ks == nil || len(ks.byKID) == 0 {
		return Claims{}, ErrBadSignature
	}

	// The signature covers the encoded (not decoded) header and payload, per
	// RFC 7515.
	signingInput := []byte(headerB64 + "." + payloadB64)
	verified := false
	for _, pub := range ks.candidates(header.Kid) {
		if ed25519.Verify(pub, signingInput, sig) {
			verified = true
			break
		}
	}
	if !verified {
		return Claims{}, ErrBadSignature
	}

	// Only now, with a verified signature, is the payload trusted enough to
	// parse and check.
	payloadRaw, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: payload: %w", ErrMalformedToken, err)
	}
	var claims jwsClaims
	if err := json.Unmarshal(payloadRaw, &claims); err != nil {
		return Claims{}, fmt.Errorf("%w: payload: %w", ErrMalformedToken, err)
	}

	if claims.Exp == 0 {
		return Claims{}, fmt.Errorf("%w: missing exp", ErrMalformedToken)
	}
	skew := opts.Skew
	if skew < 0 {
		skew = 0
	}
	exp := time.Unix(claims.Exp, 0)
	if now().After(exp.Add(skew)) {
		return Claims{}, ErrTokenExpired
	}

	if claims.Iss != opts.Issuer {
		return Claims{}, ErrIssuerMismatch
	}
	if claims.Aud != opts.Audience {
		return Claims{}, ErrAudienceMismatch
	}
	if claims.Sub != opts.Subject {
		return Claims{}, ErrSubjectMismatch
	}

	return Claims{
		Issuer:    claims.Iss,
		Subject:   claims.Sub,
		Audience:  claims.Aud,
		ExpiresAt: exp,
	}, nil
}
