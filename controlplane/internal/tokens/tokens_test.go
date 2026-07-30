package tokens

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/keys"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/storage/sqlite"
)

const testAccountID = "acct_test"

func newTestIssuer(t *testing.T) (*Issuer, *keys.Manager, *sql.DB) {
	t.Helper()

	db, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sqlite.Open() unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(
		`INSERT INTO accounts (id, google_subject, email, created_at) VALUES (?, ?, ?, ?)`,
		testAccountID, "google-subject", "test@example.test", time.Now().UTC(),
	); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	km, err := keys.Load(t.TempDir())
	if err != nil {
		t.Fatalf("keys.Load() unexpected error: %v", err)
	}

	return NewIssuer(km, db, "https://ao.agentlab.in", 15*time.Minute), km, db
}

func decodeJWT(t *testing.T, token string) (header jwtHeader, claims accessClaims, signingInput, sig []byte) {
	t.Helper()

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3: %q", len(parts), token)
	}

	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if err := json.Unmarshal(rawHeader, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}

	rawClaims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	if err := json.Unmarshal(rawClaims, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}

	sig, err = base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}

	return header, claims, []byte(parts[0] + "." + parts[1]), sig
}

func TestIssueAccessToken_ClaimsMatchContract(t *testing.T) {
	issuer, km, _ := newTestIssuer(t)

	before := time.Now().UTC()
	token, err := issuer.IssueAccessToken(testAccountID, "machine-1")
	if err != nil {
		t.Fatalf("IssueAccessToken() unexpected error: %v", err)
	}
	after := time.Now().UTC()

	header, claims, signingInput, sig := decodeJWT(t, token)

	if header.Alg != "EdDSA" {
		t.Errorf("alg = %q, want EdDSA", header.Alg)
	}
	if header.Typ != "JWT" {
		t.Errorf("typ = %q, want JWT", header.Typ)
	}
	wantKID, priv := km.Active()
	if header.Kid != wantKID {
		t.Errorf("kid = %q, want %q", header.Kid, wantKID)
	}

	if claims.Iss != "https://ao.agentlab.in" {
		t.Errorf("iss = %q, want https://ao.agentlab.in", claims.Iss)
	}
	if claims.Sub != testAccountID {
		t.Errorf("sub = %q, want %q", claims.Sub, testAccountID)
	}
	if claims.Aud != "machine-1" {
		t.Errorf("aud = %q, want machine-1", claims.Aud)
	}
	if claims.Jti == "" {
		t.Error("jti is empty, want a unique id")
	}

	wantExpFloor := before.Add(15 * time.Minute).Unix()
	wantExpCeil := after.Add(15 * time.Minute).Unix()
	if claims.Exp < wantExpFloor || claims.Exp > wantExpCeil {
		t.Errorf("exp = %d, want between %d and %d", claims.Exp, wantExpFloor, wantExpCeil)
	}
	if claims.Iat < before.Unix() || claims.Iat > after.Unix() {
		t.Errorf("iat = %d, want between %d and %d", claims.Iat, before.Unix(), after.Unix())
	}

	if !ed25519.Verify(priv.Public().(ed25519.PublicKey), signingInput, sig) {
		t.Error("signature does not verify against the active key published in JWKS")
	}
}

func TestIssueAccessToken_DefaultTTLAppliedWhenZero(t *testing.T) {
	db, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sqlite.Open() unexpected error: %v", err)
	}
	defer db.Close()
	km, err := keys.Load(t.TempDir())
	if err != nil {
		t.Fatalf("keys.Load() unexpected error: %v", err)
	}

	issuer := NewIssuer(km, db, "https://ao.agentlab.in", 0)
	if issuer.accessTTL != DefaultAccessTokenTTL {
		t.Errorf("accessTTL = %v, want default %v", issuer.accessTTL, DefaultAccessTokenTTL)
	}
}

func TestIssueRefreshToken_StoresOnlyTheHash(t *testing.T) {
	issuer, _, db := newTestIssuer(t)

	plaintext, err := issuer.IssueRefreshToken(context.Background(), testAccountID, "install-1")
	if err != nil {
		t.Fatalf("IssueRefreshToken() unexpected error: %v", err)
	}
	if plaintext == "" {
		t.Fatal("IssueRefreshToken() returned empty token")
	}

	var storedHash, accountID, installID string
	err = db.QueryRow(
		`SELECT token_hash, account_id, install_id FROM refresh_tokens WHERE account_id = ?`, testAccountID,
	).Scan(&storedHash, &accountID, &installID)
	if err != nil {
		t.Fatalf("query stored refresh token: %v", err)
	}

	if storedHash == plaintext {
		t.Fatal("plaintext token stored verbatim in refresh_tokens, want it hashed")
	}
	if storedHash != hashToken(plaintext) {
		t.Error("stored hash does not match hashToken(plaintext)")
	}
	if installID != "install-1" {
		t.Errorf("install_id = %q, want install-1", installID)
	}
}

func TestRotateRefreshToken_RotatesAndRevokesThePrevious(t *testing.T) {
	issuer, _, _ := newTestIssuer(t)
	ctx := context.Background()

	original, err := issuer.IssueRefreshToken(ctx, testAccountID, "install-1")
	if err != nil {
		t.Fatalf("IssueRefreshToken() unexpected error: %v", err)
	}

	rotated, accountID, installID, err := issuer.RotateRefreshToken(ctx, original)
	if err != nil {
		t.Fatalf("RotateRefreshToken() unexpected error: %v", err)
	}
	if rotated == original {
		t.Fatal("RotateRefreshToken() returned the same token")
	}
	if accountID != testAccountID {
		t.Errorf("accountID = %q, want %q", accountID, testAccountID)
	}
	if installID != "install-1" {
		t.Errorf("installID = %q, want install-1", installID)
	}

	// The original token is now revoked and cannot be exchanged again.
	if _, _, _, err := issuer.RotateRefreshToken(ctx, original); err != ErrRefreshTokenRevoked {
		t.Errorf("re-rotating the original token: err = %v, want ErrRefreshTokenRevoked", err)
	}

	// The rotated token works and itself rotates cleanly.
	if _, _, _, err := issuer.RotateRefreshToken(ctx, rotated); err != nil {
		t.Errorf("rotating the new token: unexpected error: %v", err)
	}
}

func TestRotateRefreshToken_UnknownTokenRejected(t *testing.T) {
	issuer, _, _ := newTestIssuer(t)

	_, _, _, err := issuer.RotateRefreshToken(context.Background(), "not-a-real-token")
	if err != ErrInvalidRefreshToken {
		t.Errorf("err = %v, want ErrInvalidRefreshToken", err)
	}
}

func TestRotateRefreshToken_ExpiredTokenRejected(t *testing.T) {
	issuer, _, db := newTestIssuer(t)
	ctx := context.Background()

	plaintext, err := issuer.IssueRefreshToken(ctx, testAccountID, "install-1")
	if err != nil {
		t.Fatalf("IssueRefreshToken() unexpected error: %v", err)
	}

	if _, err := db.Exec(
		`UPDATE refresh_tokens SET expires_at = ? WHERE token_hash = ?`,
		time.Now().UTC().Add(-time.Hour), hashToken(plaintext),
	); err != nil {
		t.Fatalf("force-expire token: %v", err)
	}

	if _, _, _, err := issuer.RotateRefreshToken(ctx, plaintext); err != ErrRefreshTokenExpired {
		t.Errorf("err = %v, want ErrRefreshTokenExpired", err)
	}
}

func TestRevokeRefreshToken_IsIdempotentAndRejectsUnknown(t *testing.T) {
	issuer, _, _ := newTestIssuer(t)
	ctx := context.Background()

	plaintext, err := issuer.IssueRefreshToken(ctx, testAccountID, "install-1")
	if err != nil {
		t.Fatalf("IssueRefreshToken() unexpected error: %v", err)
	}

	if err := issuer.RevokeRefreshToken(ctx, plaintext); err != nil {
		t.Fatalf("first RevokeRefreshToken() unexpected error: %v", err)
	}
	if err := issuer.RevokeRefreshToken(ctx, plaintext); err != nil {
		t.Fatalf("second RevokeRefreshToken() (idempotent) unexpected error: %v", err)
	}

	if _, _, _, err := issuer.RotateRefreshToken(ctx, plaintext); err != ErrRefreshTokenRevoked {
		t.Errorf("rotating a revoked token: err = %v, want ErrRefreshTokenRevoked", err)
	}

	if err := issuer.RevokeRefreshToken(ctx, "not-a-real-token"); err != ErrInvalidRefreshToken {
		t.Errorf("revoking an unknown token: err = %v, want ErrInvalidRefreshToken", err)
	}
}

func TestAccountForRefreshToken_ValidatesWithoutRotating(t *testing.T) {
	issuer, _, db := newTestIssuer(t)
	ctx := context.Background()

	plaintext, err := issuer.IssueRefreshToken(ctx, testAccountID, "install-1")
	if err != nil {
		t.Fatalf("IssueRefreshToken() unexpected error: %v", err)
	}

	// Looking the account up twice must both work: unlike RotateRefreshToken,
	// this does not consume the presented token.
	for i := range 2 {
		accountID, err := issuer.AccountForRefreshToken(ctx, plaintext)
		if err != nil {
			t.Fatalf("AccountForRefreshToken() call %d unexpected error: %v", i+1, err)
		}
		if accountID != testAccountID {
			t.Errorf("accountID = %q, want %q", accountID, testAccountID)
		}
	}

	if _, err := issuer.AccountForRefreshToken(ctx, "not-a-real-token"); err != ErrInvalidRefreshToken {
		t.Errorf("unknown token: err = %v, want ErrInvalidRefreshToken", err)
	}

	if _, err := db.Exec(
		`UPDATE refresh_tokens SET expires_at = ? WHERE token_hash = ?`,
		time.Now().UTC().Add(-time.Hour), hashToken(plaintext),
	); err != nil {
		t.Fatalf("force-expire token: %v", err)
	}
	if _, err := issuer.AccountForRefreshToken(ctx, plaintext); err != ErrRefreshTokenExpired {
		t.Errorf("expired token: err = %v, want ErrRefreshTokenExpired", err)
	}

	if err := issuer.RevokeRefreshToken(ctx, plaintext); err != nil {
		t.Fatalf("RevokeRefreshToken() unexpected error: %v", err)
	}
	if _, err := issuer.AccountForRefreshToken(ctx, plaintext); err != ErrRefreshTokenRevoked {
		t.Errorf("revoked token: err = %v, want ErrRefreshTokenRevoked", err)
	}
}
