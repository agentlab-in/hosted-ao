package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// DefaultRefreshTokenTTL is how long a refresh token is valid from issuance
// if never rotated or revoked first. Refresh tokens rotate on every use, so
// in practice this mostly bounds how long a desktop install can stay signed
// in without contacting the control plane at all.
const DefaultRefreshTokenTTL = 90 * 24 * time.Hour

// refreshTokenBytes is the entropy of an opaque refresh token before
// base64url encoding: 32 bytes is 256 bits.
const refreshTokenBytes = 32

var (
	// ErrInvalidRefreshToken is returned when a presented refresh token does
	// not match any stored hash.
	ErrInvalidRefreshToken = errors.New("invalid refresh token")
	// ErrRefreshTokenRevoked is returned when a presented refresh token
	// matches a row that has already been revoked, including by a prior
	// rotation, so it cannot be exchanged again.
	ErrRefreshTokenRevoked = errors.New("refresh token revoked")
	// ErrRefreshTokenExpired is returned when a presented refresh token has
	// passed its expiry.
	ErrRefreshTokenExpired = errors.New("refresh token expired")
)

// execer is the write half shared by *sql.DB and *sql.Tx, so one insert
// serves both the standalone issue path and a caller's transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// IssueRefreshToken mints a new opaque refresh token bound to accountID and
// installID, stores only its hash in refresh_tokens, and returns the
// plaintext token. The plaintext is never stored and cannot be recovered
// once returned.
func (i *Issuer) IssueRefreshToken(ctx context.Context, accountID, installID string) (string, error) {
	return issueRefreshToken(ctx, i.db, accountID, installID)
}

// IssueRefreshTokenTx is IssueRefreshToken inside a transaction the caller
// already owns, so consuming a one-time credential and issuing the refresh
// token it yields commit together or not at all.
//
// The desktop login exchange needs exactly that: it deletes the authorization
// code and inserts this row in one commit, so a replay of the same code cannot
// mint a second refresh token no matter how the two requests interleave.
func (i *Issuer) IssueRefreshTokenTx(ctx context.Context, tx *sql.Tx, accountID, installID string) (string, error) {
	return issueRefreshToken(ctx, tx, accountID, installID)
}

func issueRefreshToken(ctx context.Context, db execer, accountID, installID string) (string, error) {
	plaintext, err := randomToken()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	_, err = db.ExecContext(ctx,
		`INSERT INTO refresh_tokens (id, account_id, install_id, token_hash, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), accountID, installID, hashToken(plaintext), now, now.Add(DefaultRefreshTokenTTL),
	)
	if err != nil {
		return "", fmt.Errorf("insert refresh token: %w", err)
	}
	return plaintext, nil
}

// RotateRefreshToken validates a presented refresh token, revokes it, and
// issues a replacement bound to the same account and install, per the token
// contract's rotate-on-use rule. It returns the new plaintext token and the
// account and install it belongs to, so the caller can also mint a fresh
// access token for the same account.
func (i *Issuer) RotateRefreshToken(ctx context.Context, presented string) (newToken, accountID, installID string, err error) {
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("begin rotation tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		id        string
		expiresAt sql.NullTime
		revokedAt sql.NullTime
	)
	row := tx.QueryRowContext(ctx,
		`SELECT id, account_id, install_id, expires_at, revoked_at FROM refresh_tokens WHERE token_hash = ?`,
		hashToken(presented),
	)
	if err := row.Scan(&id, &accountID, &installID, &expiresAt, &revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", "", ErrInvalidRefreshToken
		}
		return "", "", "", fmt.Errorf("look up refresh token: %w", err)
	}
	if revokedAt.Valid {
		return "", "", "", ErrRefreshTokenRevoked
	}
	if expiresAt.Valid && time.Now().UTC().After(expiresAt.Time) {
		return "", "", "", ErrRefreshTokenExpired
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked_at = ?, last_used_at = ? WHERE id = ?`, now, now, id,
	); err != nil {
		return "", "", "", fmt.Errorf("revoke rotated refresh token: %w", err)
	}

	newToken, err = randomToken()
	if err != nil {
		return "", "", "", err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO refresh_tokens (id, account_id, install_id, token_hash, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), accountID, installID, hashToken(newToken), now, now.Add(DefaultRefreshTokenTTL),
	); err != nil {
		return "", "", "", fmt.Errorf("insert rotated refresh token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", "", "", fmt.Errorf("commit rotation: %w", err)
	}
	return newToken, accountID, installID, nil
}

// RevokeRefreshToken revokes a refresh token so it can no longer be
// exchanged. It is idempotent: revoking an already-revoked token is not an
// error. Revoking a token whose hash matches no row returns
// ErrInvalidRefreshToken.
func (i *Issuer) RevokeRefreshToken(ctx context.Context, presented string) error {
	hash := hashToken(presented)

	res, err := i.db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`,
		time.Now().UTC(), hash,
	)
	if err != nil {
		return fmt.Errorf("revoke refresh token: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}

	var exists bool
	if err := i.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM refresh_tokens WHERE token_hash = ?)`, hash,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check refresh token existence: %w", err)
	}
	if !exists {
		return ErrInvalidRefreshToken
	}
	return nil // already revoked
}

func randomToken() (string, error) {
	buf := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate refresh token entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
