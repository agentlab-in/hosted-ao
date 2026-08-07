package tokens

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/keys"
)

// This file owns the golden cross-module contract fixtures. controlplane/ and
// backend/ are separate Go modules that cannot import each other, so the only
// way a control-plane-produced artifact reaches the gateway's verifier is by
// committing it. These fixtures are that artifact, produced here by the real
// issuing code paths (keys.Manager.JWKS and Issuer.IssueAccessToken) and
// consumed by TestGolden_* in backend/internal/vmgateway.
//
// Regenerate with:
//
//	cd controlplane && go test ./internal/tokens/ -run TestGoldenFixtures -update
//
// Then review the diff. jwks.json is byte stable across regenerations (the
// keys are derived from the committed seed phrases below), so any diff in it
// is a real contract change. The two .jwt files always change, because iat,
// exp, and jti are minted from the current time and a fresh uuid.
const goldenDir = "../../../backend/internal/vmgateway/testdata"

var updateGolden = flag.Bool("update", false, "regenerate the golden cross-module fixtures in "+goldenDir)

// The claim values the fixtures are minted with. The vmgateway test pins the
// same three strings independently rather than reading them from a fixture,
// so a change on either side of the contract shows up as a failure instead of
// as two files agreeing with each other about the wrong value.
const (
	goldenIssuer    = "https://ao.agentlab.in"
	goldenAccountID = "6f1d3b0c-2a5e-4c9f-8b71-0d2e4a6c8f10"
	goldenMachineID = "b3c1a4d2-7e60-4f38-9a15-2c8d5e7f0b94"
)

// goldenTTL puts the fixture's exp roughly a century out so the committed
// token does not rot and the gateway test can exercise the real expiry check
// instead of stubbing the clock. Production TTL is validated to 10 to 30
// minutes in config.Load, not here, so passing a large TTL to NewIssuer
// bypasses no production guard.
const goldenTTL = 100 * 365 * 24 * time.Hour

// Test-only signing key seeds. The private keys are derived at test time from
// these phrases (seed = sha256(phrase)), so no key material is committed:
// there is nothing here to leak, and the derived keys sign nothing but
// fixtures. Do not reuse them for anything else.
const (
	goldenActivePhrase  = "hosted-ao golden fixture active signing key, test only, not a secret"
	goldenNextPhrase    = "hosted-ao golden fixture next signing key, test only, not a secret"
	goldenForeignActive = "hosted-ao golden fixture foreign active key, test only, not a secret"
	goldenForeignNext   = "hosted-ao golden fixture foreign next key, test only, not a secret"
)

// onDiskKeyPair mirrors the JSON shape keys.Manager persists under
// dataDir/keys, which is how a deterministic key gets into a Manager: the
// package generates from crypto/rand with no injection point. If that shape
// ever drifts, seededManager's assertions below fail loudly rather than
// silently signing with a zero key.
type onDiskKeyPair struct {
	KID        string             `json:"kid"`
	PrivateKey ed25519.PrivateKey `json:"private_key"`
	PublicKey  ed25519.PublicKey  `json:"public_key"`
	CreatedAt  time.Time          `json:"created_at"`
}

func seededKeyPair(phrase string) onDiskKeyPair {
	seed := sha256.Sum256([]byte(phrase))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)
	// Deliberately recomputed here rather than calling keys.kidFor, which is
	// unexported. TestGoldenJWKS_MirrorsFixture in the keys package asserts
	// the committed kid still equals kidFor(x), so a change to the real kid
	// derivation cannot pass unnoticed just because this copy agrees with the
	// old rule.
	sum := sha256.Sum256(pub)
	return onDiskKeyPair{
		KID:        hex.EncodeToString(sum[:])[:16],
		PrivateKey: priv,
		PublicKey:  pub,
		CreatedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// seededManager returns a real keys.Manager holding the two seed-derived key
// pairs, by writing them where keys.Load reads from.
func seededManager(t *testing.T, activePhrase, nextPhrase string) *keys.Manager {
	t.Helper()

	dataDir := t.TempDir()
	keyDir := filepath.Join(dataDir, "keys")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatalf("create key dir: %v", err)
	}
	active := seededKeyPair(activePhrase)
	for name, kp := range map[string]onDiskKeyPair{
		"active.json": active,
		"next.json":   seededKeyPair(nextPhrase),
	} {
		raw, err := json.Marshal(kp)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(keyDir, name), raw, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	m, err := keys.Load(dataDir)
	if err != nil {
		t.Fatalf("keys.Load: %v", err)
	}
	kid, priv := m.Active()
	if kid != active.KID {
		t.Fatalf("seeded manager reports kid %q, want %q: the on-disk key format this test writes has drifted from what keys.Load reads", kid, active.KID)
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Fatalf("seeded manager has a %d byte private key, want %d: the on-disk key format this test writes has drifted from what keys.Load reads", len(priv), ed25519.PrivateKeySize)
	}
	return m
}

// encodeJWKS produces exactly the bytes GET /.well-known/jwks.json serves, so
// the fixture is the wire document and not a re-serialization of it.
func encodeJWKS(t *testing.T, m *keys.Manager) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(m.JWKS()); err != nil {
		t.Fatalf("encode jwks: %v", err)
	}
	return buf.Bytes()
}

// TestGoldenFixtures regenerates the fixtures under -update, and otherwise
// asserts the committed ones are still what today's issuer produces.
func TestGoldenFixtures(t *testing.T) {
	km := seededManager(t, goldenActivePhrase, goldenNextPhrase)
	// db is nil: IssueAccessToken signs from the key manager and never
	// touches storage. Only refresh tokens do.
	issuer := NewIssuer(km, nil, goldenIssuer, goldenTTL)
	jwks := encodeJWKS(t, km)

	if *updateGolden {
		token, err := issuer.IssueAccessToken(goldenAccountID, goldenMachineID)
		if err != nil {
			t.Fatalf("IssueAccessToken: %v", err)
		}
		// Same claims, signed by keys that are not in jwks.json: the
		// gateway's negative case. Minted through the same real path so it is
		// rejected for its signature alone.
		foreign, err := NewIssuer(
			seededManager(t, goldenForeignActive, goldenForeignNext), nil, goldenIssuer, goldenTTL,
		).IssueAccessToken(goldenAccountID, goldenMachineID)
		if err != nil {
			t.Fatalf("IssueAccessToken (foreign): %v", err)
		}
		writeGolden(t, "jwks.json", jwks)
		writeGolden(t, "access_token.jwt", []byte(token+"\n"))
		writeGolden(t, "foreign_token.jwt", []byte(foreign+"\n"))
		t.Logf("regenerated golden fixtures in %s; review the diff before committing", goldenDir)
		return
	}

	if got := readGolden(t, "jwks.json"); !bytes.Equal(got, jwks) {
		t.Fatalf("committed jwks.json is not what keys.Manager.JWKS() serves today.\n got: %s\nwant: %s\nIf this is an intended contract change, regenerate:\n  cd controlplane && go test ./internal/tokens/ -run TestGoldenFixtures -update", got, jwks)
	}

	header, claims, signingInput, sig := decodeJWT(t, string(bytes.TrimSpace(readGolden(t, "access_token.jwt"))))
	activeKID, activePriv := km.Active()
	pub := activePriv.Public().(ed25519.PublicKey)
	if !ed25519.Verify(pub, signingInput, sig) {
		t.Fatal("committed access_token.jwt does not verify against the fixture's active key")
	}
	if header.Alg != "EdDSA" || header.Typ != "JWT" || header.Kid != activeKID {
		t.Fatalf("committed token header = %+v, want alg EdDSA, typ JWT, kid %s", header, activeKID)
	}
	if claims.Iss != goldenIssuer || claims.Sub != goldenAccountID || claims.Aud != goldenMachineID {
		t.Fatalf("committed token claims = %+v, want iss %s, sub %s, aud %s", claims, goldenIssuer, goldenAccountID, goldenMachineID)
	}
	if claims.Jti == "" || claims.Iat == 0 {
		t.Fatalf("committed token is missing jti or iat: %+v", claims)
	}
	// A decade of headroom, so the fixture is regenerated long before an
	// expired one starts failing the gateway test for the wrong reason.
	if until := time.Until(time.Unix(claims.Exp, 0)); until < 10*365*24*time.Hour {
		t.Fatalf("committed token expires in %s, too soon to stay a useful fixture; regenerate it", until)
	}
}

func writeGolden(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(goldenDir, 0o755); err != nil {
		t.Fatalf("create %s: %v", goldenDir, err)
	}
	if err := os.WriteFile(filepath.Join(goldenDir, name), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(goldenDir, name))
	if err != nil {
		t.Fatalf("read golden fixture: %v\nGenerate it with:\n  cd controlplane && go test ./internal/tokens/ -run TestGoldenFixtures -update", err)
	}
	return data
}
