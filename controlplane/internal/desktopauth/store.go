package desktopauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
)

// authCodeBytes is the entropy of an authorization code before base64url
// encoding: 32 bytes is 256 bits, the same as a device code and a refresh
// token, so the code itself is not guessable and the exchange's rate limit is
// about load rather than brute force.
const authCodeBytes = 32

// errInvalidCode is every reason an exchange can fail: no such code, expired,
// already redeemed, wrong redirect URI, wrong verifier. They are one error on
// purpose, so the endpoint above cannot accidentally tell them apart. See
// handleToken.
var errInvalidCode = errors.New("authorization code is not valid")

// newAuthCode returns a fresh authorization code, the bearer secret that
// travels back through the browser to the app's loopback listener.
func newAuthCode() (string, error) {
	buf := make([]byte, authCodeBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate authorization code: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashCode is how a code is stored and looked up. The plaintext is never
// written to the database, so a leak of controlplane.db hands over no live
// codes, and the lookup compares a fixed-width digest rather than the secret.
func hashCode(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// createCode issues an authorization code bound to accountID, redirectURI, and
// challenge, and returns the plaintext. Only the hash is stored, so the
// returned value cannot be recovered afterwards.
func (s *Service) createCode(ctx context.Context, accountID, redirectURI, challenge string, now time.Time) (string, error) {
	code, err := newAuthCode()
	if err != nil {
		return "", err
	}

	s.sweepExpired(ctx, now)

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO desktop_auth_codes
		   (code_hash, account_id, redirect_uri, code_challenge, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hashCode(code), accountID, redirectURI, challenge, now, now.Add(authCodeTTL),
	); err != nil {
		return "", fmt.Errorf("insert authorization code: %w", err)
	}
	return code, nil
}

// sweepExpired deletes codes that are past their expiry. It runs on the issue
// path because that is where the table grows, and one DELETE over an indexed
// comparison is cheaper than the row it is about to insert. An expired row
// carries nothing: redeem rejects one regardless, so deleting it changes no
// answer this service gives. A failure is logged and swallowed, because a
// sweep that did not run must not fail the sign-in that triggered it.
func (s *Service) sweepExpired(ctx context.Context, now time.Time) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM desktop_auth_codes WHERE expires_at < ?`, now); err != nil {
		log.Printf("desktopauth: sweep expired authorization codes: %v", err)
	}
}

// redeemed is what a successful exchange yields: the account the code was
// bound to, and the refresh token issued for it.
type redeemed struct {
	AccountID    string
	Email        string
	RefreshToken string
}

// redeem verifies a presented code against everything it was bound to,
// consumes it, and issues the refresh token, all in one transaction.
//
// The consume and the issue commit together or not at all, which is the whole
// point: two requests presenting the same code both open an immediate write
// transaction (see the _txlock=immediate reasoning in storage/sqlite), so they
// serialize, the first deletes the row and inserts a refresh token, and the
// second finds nothing and mints none. A crash between the two leaves the code
// intact rather than spent, which is recoverable; the reverse would not be.
//
// A failed verifier or redirect URI does not consume the code. The code alone
// is useless without the verifier, so burning it on a bad attempt would only
// give anyone who intercepted the code a way to deny the real client its
// sign-in.
//
// Every failure returns errInvalidCode, including an unknown code. The caller
// must not be able to learn which of the checks it failed.
func (s *Service) redeem(ctx context.Context, code, verifier, redirectURI string, now time.Time) (redeemed, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return redeemed{}, fmt.Errorf("begin redemption tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		accountID       string
		storedRedirect  string
		storedChallenge string
		expiresAt       time.Time
	)
	hash := hashCode(code)
	err = tx.QueryRowContext(ctx,
		`SELECT account_id, redirect_uri, code_challenge, expires_at
		   FROM desktop_auth_codes WHERE code_hash = ?`, hash,
	).Scan(&accountID, &storedRedirect, &storedChallenge, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return redeemed{}, errInvalidCode
	}
	if err != nil {
		return redeemed{}, fmt.Errorf("look up authorization code: %w", err)
	}

	if !now.Before(expiresAt) {
		return redeemed{}, errInvalidCode
	}
	// Simple string comparison, per RFC 6749 section 4.1.3: the value the app
	// sends here must be the one it asked with, byte for byte.
	if subtle.ConstantTimeCompare([]byte(storedRedirect), []byte(redirectURI)) != 1 {
		return redeemed{}, errInvalidCode
	}
	// RFC 7636 section 4.6. Constant time because the comparison is cheap and
	// the alternative is reasoning about whether a challenge is secret enough
	// for an early exit to be harmless.
	if subtle.ConstantTimeCompare([]byte(storedChallenge), []byte(challengeFor(verifier))) != 1 {
		return redeemed{}, errInvalidCode
	}

	res, err := tx.ExecContext(ctx, `DELETE FROM desktop_auth_codes WHERE code_hash = ?`, hash)
	if err != nil {
		return redeemed{}, fmt.Errorf("consume authorization code: %w", err)
	}
	// Belt to the transaction's braces: if the row is gone by now, someone else
	// redeemed it and this request must mint nothing.
	if n, _ := res.RowsAffected(); n != 1 {
		return redeemed{}, errInvalidCode
	}

	var email string
	if err := tx.QueryRowContext(ctx, `SELECT email FROM accounts WHERE id = ?`, accountID).Scan(&email); err != nil {
		return redeemed{}, fmt.Errorf("look up account for authorization code: %w", err)
	}

	// The contract carries no install identifier through the login exchange, so
	// each completed sign-in is its own install: a fresh id here means revoking
	// one desktop's refresh token chain never touches another's.
	refreshToken, err := s.issuer.IssueRefreshTokenTx(ctx, tx, accountID, uuid.NewString())
	if err != nil {
		return redeemed{}, fmt.Errorf("issue refresh token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return redeemed{}, fmt.Errorf("commit redemption: %w", err)
	}
	return redeemed{AccountID: accountID, Email: email, RefreshToken: refreshToken}, nil
}
