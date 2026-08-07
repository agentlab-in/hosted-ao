package tokens

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// There are exactly two access token audiences, and they are not
// interchangeable in either direction. See TOKEN_CONTRACT.md.
//
//   - `aud` = machines.id authorizes a call to that machine's gateway.
//     IssueAccessToken mints these.
//   - `aud` = the control plane's own origin authorizes a call to the control
//     plane's API. IssueControlPlaneToken mints these.
//
// A control-plane token replayed against a VM fails the gateway's `aud` check
// against its machine id, and a machine token presented to the control plane
// API fails VerifyControlPlaneToken. Neither rejection needs any coordination
// between the two services beyond this contract.

var (
	// ErrTokenMalformed is returned when a presented token is not a
	// well-formed compact JWT, or does not use the algorithm and key this
	// control plane signs with.
	ErrTokenMalformed = errors.New("malformed token")
	// ErrTokenSignature is returned when a presented token's signature does
	// not verify against the published key its header names.
	ErrTokenSignature = errors.New("invalid token signature")
	// ErrTokenClaims is returned when a presented token verifies but is
	// addressed elsewhere: a different issuer, or another audience, which is
	// what a machine-audience token presented to the control plane API is.
	ErrTokenClaims = errors.New("token is not addressed to this service")
	// ErrTokenExpired is returned when a presented token is past its `exp`,
	// or carries no `exp` at all. A missing expiry is a rejection rather than
	// "no expiry".
	ErrTokenExpired = errors.New("token expired")
)

// IssueControlPlaneToken mints an access token that authorizes accountID to
// call the control plane's own API. It is the same EdDSA JWT as
// IssueAccessToken, signed with the same key and carrying the same iss, exp,
// iat, and jti, and differs only in `aud`: the control plane's origin rather
// than a machine id.
//
// The desktop obtains one by exchanging its refresh token, which is the only
// place a refresh token is ever presented. A refresh token is not a
// credential for any resource route.
func (i *Issuer) IssueControlPlaneToken(accountID string) (string, error) {
	kid, priv := i.keys.Active()
	now := time.Now().UTC()
	claims := accessClaims{
		Iss: i.issuer,
		Sub: accountID,
		Aud: i.issuer,
		Exp: now.Add(i.accessTTL).Unix(),
		Iat: now.Unix(),
		Jti: uuid.NewString(),
	}
	return signEdDSA(kid, priv, claims)
}

// VerifyControlPlaneToken checks a presented bearer token and returns the
// account it authorizes.
//
// Signature first, claims after, matching the order TOKEN_CONTRACT.md
// prescribes for the gateway: a token whose claims are read before its
// signature is a token an attacker gets to write. The audience must be this
// control plane's origin, so a machine-audience token, however validly
// signed, is rejected here.
func (i *Issuer) VerifyControlPlaneToken(token string) (accountID string, err error) {
	rawHeader, rawClaims, sig, signingInput, err := splitJWT(token)
	if err != nil {
		return "", err
	}

	var header jwtHeader
	if err := json.Unmarshal(rawHeader, &header); err != nil {
		return "", ErrTokenMalformed
	}
	if header.Alg != "EdDSA" {
		// Anything else, "none" above all, is not something this service
		// signs, so there is no key to check it against.
		return "", ErrTokenMalformed
	}
	pub, ok := i.keys.PublicKey(header.Kid)
	if !ok {
		return "", ErrTokenMalformed
	}
	if !ed25519.Verify(pub, signingInput, sig) {
		return "", ErrTokenSignature
	}

	var claims accessClaims
	if err := json.Unmarshal(rawClaims, &claims); err != nil {
		return "", ErrTokenMalformed
	}
	if claims.Iss != i.issuer || claims.Aud != i.issuer {
		return "", ErrTokenClaims
	}
	if claims.Exp == 0 || time.Now().UTC().Unix() >= claims.Exp {
		return "", ErrTokenExpired
	}
	if claims.Sub == "" {
		return "", ErrTokenClaims
	}
	return claims.Sub, nil
}

// splitJWT breaks a compact JWT into its decoded parts plus the bytes the
// signature covers.
func splitJWT(token string) (header, claims, sig, signingInput []byte, err error) {
	rawHeader, rest, ok := strings.Cut(token, ".")
	if !ok {
		return nil, nil, nil, nil, ErrTokenMalformed
	}
	rawClaims, rawSig, ok := strings.Cut(rest, ".")
	if !ok || strings.Contains(rawSig, ".") {
		return nil, nil, nil, nil, ErrTokenMalformed
	}

	decoded := make([][]byte, 0, 3)
	for _, part := range []string{rawHeader, rawClaims, rawSig} {
		b, decodeErr := base64.RawURLEncoding.DecodeString(part)
		if decodeErr != nil {
			return nil, nil, nil, nil, fmt.Errorf("%w: %v", ErrTokenMalformed, decodeErr)
		}
		decoded = append(decoded, b)
	}
	return decoded[0], decoded[1], decoded[2], []byte(rawHeader + "." + rawClaims), nil
}
