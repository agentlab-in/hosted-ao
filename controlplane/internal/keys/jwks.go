package keys

import "encoding/base64"

// jwk is one entry of a JSON Web Key Set, RFC 8037 shape for an OKP
// (Ed25519) public key: only the public half is ever published.
type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	X   string `json:"x"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

type jwksDoc struct {
	Keys []jwk `json:"keys"`
}

// JWKS returns the JSON Web Key Set publishing the active and next public
// keys, so a verifier can resolve either kid.
func (m *Manager) JWKS() jwksDoc {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return jwksDoc{Keys: []jwk{toJWK(m.active), toJWK(m.next)}}
}

func toJWK(kp keyPair) jwk {
	return jwk{
		Kty: "OKP",
		Crv: "Ed25519",
		Kid: kp.KID,
		X:   base64.RawURLEncoding.EncodeToString(kp.PublicKey),
		Use: "sig",
		Alg: "EdDSA",
	}
}
