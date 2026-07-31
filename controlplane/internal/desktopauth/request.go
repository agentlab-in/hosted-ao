package desktopauth

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
)

// maxStateLen caps the state the app asks to have echoed back. state is
// opaque to this service and only the app interprets it, so the only thing
// worth enforcing is that it exists and cannot be used to push an unbounded
// string through a Location header. The contract's own state is 32 CSPRNG
// bytes base64url, which is 43 characters.
const maxStateLen = 512

var (
	errRedirectMissing     = errors.New("redirect_uri is required")
	errRedirectMalformed   = errors.New("redirect_uri is not a URL")
	errRedirectNotHTTP     = errors.New("redirect_uri must use the http scheme")
	errRedirectNotLoopback = errors.New("redirect_uri host must be 127.0.0.1 or [::1]")
	errRedirectHasUser     = errors.New("redirect_uri must not carry userinfo")
	errRedirectHasQuery    = errors.New("redirect_uri must not carry a query")
	errRedirectHasFragment = errors.New("redirect_uri must not carry a fragment")
)

// validateLoopbackRedirect returns raw unchanged if it is a redirect target
// this endpoint may send an account to, and an error naming the reason if it
// is not.
//
// RFC 8252 section 7.3: a native app receives its redirect on a loopback
// listener bound to an ephemeral port, so the port is whatever the OS handed
// the app that run and cannot be registered in advance. The port is therefore
// the only part left free. The host is pinned to the two loopback literals and
// nothing else: "localhost" is refused because it is a name, and section 8.3
// warns that what it resolves to is not this service's decision. Anything
// beyond that is an open redirect on a public endpoint that ends in a refresh
// token, which is an account handed to whoever crafted the link.
//
// The value is returned verbatim rather than normalized, because the token
// endpoint compares it to what the app sends there by simple string equality,
// per RFC 6749 section 4.1.3, and a normalization applied on one side and not
// the other would break that comparison.
func validateLoopbackRedirect(raw string) (string, error) {
	if raw == "" {
		return "", errRedirectMissing
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errRedirectMalformed
	}
	// http, not https: RFC 8252 section 7.3 has the loopback interface serve
	// plaintext, and the app could not present a certificate for 127.0.0.1
	// anyway. The redirect never leaves the machine.
	if u.Scheme != "http" {
		return "", errRedirectNotHTTP
	}
	if u.User != nil {
		return "", errRedirectHasUser
	}
	if host := u.Hostname(); host != "127.0.0.1" && host != "::1" {
		return "", errRedirectNotLoopback
	}
	// url.Parse already rejects a non-numeric port, so any port that parsed
	// here is acceptable per section 7.3.
	if u.RawQuery != "" || u.ForceQuery {
		return "", errRedirectHasQuery
	}
	if u.Fragment != "" {
		return "", errRedirectHasFragment
	}
	return raw, nil
}

// validState reports whether state is one this endpoint can echo back.
func validState(state string) bool {
	return state != "" && len(state) <= maxStateLen
}

// validS256Challenge reports whether challenge is a well-formed S256 PKCE
// challenge: base64url, unpadded, of exactly one SHA-256 digest (RFC 7636
// section 4.2). Checking the shape here means a client that sent something
// else finds out at the authorization request rather than at the exchange,
// where every failure is deliberately indistinguishable.
func validS256Challenge(challenge string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil && len(raw) == sha256.Size
}

// challengeFor returns the S256 challenge a verifier produces, in the same
// encoding validS256Challenge accepts, so the exchange compares like with
// like.
func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
