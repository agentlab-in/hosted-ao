// Package tokens issues and manages the control plane's access and refresh
// tokens per TOKEN_CONTRACT.md: short-lived EdDSA JWTs for access, and
// opaque, hashed, rotating refresh tokens for long-lived desktop sessions.
package tokens

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/keys"
)

// DefaultAccessTokenTTL is used when NewIssuer is given a zero TTL. The spec
// allows this to move between 10 and 30 minutes after measuring refresh
// chatter, hence it is a parameter on Issuer rather than a hardcoded
// constant used directly by IssueAccessToken.
const DefaultAccessTokenTTL = 15 * time.Minute

// Issuer mints and manages access and refresh tokens for one control plane
// instance.
type Issuer struct {
	keys      *keys.Manager
	db        *sql.DB
	issuer    string
	accessTTL time.Duration
}

// NewIssuer builds an Issuer. issuerURL becomes the `iss` claim on every
// access token it mints (the control plane's public origin); accessTTL is
// the access token lifetime, or DefaultAccessTokenTTL if zero.
func NewIssuer(km *keys.Manager, db *sql.DB, issuerURL string, accessTTL time.Duration) *Issuer {
	if accessTTL <= 0 {
		accessTTL = DefaultAccessTokenTTL
	}
	return &Issuer{keys: km, db: db, issuer: issuerURL, accessTTL: accessTTL}
}

// AccessTokenTTL is the lifetime of the access tokens this Issuer mints. It
// is exposed so a caller returning a token to a client can report the same
// `expires_in` the token's own `exp` encodes, rather than keeping a second
// copy of the configured TTL that can drift from it.
func (i *Issuer) AccessTokenTTL() time.Duration {
	return i.accessTTL
}

type accessClaims struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
	Iat int64  `json:"iat"`
	Jti string `json:"jti"`
}

// IssueAccessToken mints an EdDSA JWT for accountID scoped to machineID, per
// TOKEN_CONTRACT.md: iss is this control plane's origin, sub is the account
// id, aud is the machine id, exp is now plus the configured access token
// lifetime, plus iat and a fresh jti.
func (i *Issuer) IssueAccessToken(accountID, machineID string) (string, error) {
	kid, priv := i.keys.Active()
	now := time.Now().UTC()
	claims := accessClaims{
		Iss: i.issuer,
		Sub: accountID,
		Aud: machineID,
		Exp: now.Add(i.accessTTL).Unix(),
		Iat: now.Unix(),
		Jti: uuid.NewString(),
	}
	return signEdDSA(kid, priv, claims)
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// signEdDSA builds and signs a compact JWT: base64url(header) + "." +
// base64url(payload) + "." + base64url(signature). Machine-audience tokens
// are verified downstream (the VM gateway, against cached JWKS); the only
// tokens this package parses are the control-plane-audience ones it is itself
// the resource server for, in controlplane.go. Both halves are small enough
// against a single algorithm that neither needs a JWT library.
func signEdDSA(kid string, priv ed25519.PrivateKey, claims any) (string, error) {
	header, err := json.Marshal(jwtHeader{Alg: "EdDSA", Kid: kid, Typ: "JWT"})
	if err != nil {
		return "", fmt.Errorf("marshal jwt header: %w", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal jwt claims: %w", err)
	}
	signingInput := b64(header) + "." + b64(payload)
	sig := ed25519.Sign(priv, []byte(signingInput))
	return signingInput + "." + b64(sig), nil
}

func b64(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
