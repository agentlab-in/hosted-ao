package device

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The states a device_codes row can be in. They match the CHECK constraint in
// 0001_init.sql.
const (
	statusPending  = "pending"
	statusApproved = "approved"
	statusDenied   = "denied"
	statusExpired  = "expired"
)

var (
	// errCodeNotFound means no device_codes row matched, either because the
	// code was never issued or because a human mistyped it.
	errCodeNotFound = errors.New("device code not found")
	// errCodeExpired means the row exists but is past expires_at. Expiry is
	// enforced on every read here, not merely displayed on the page: the sweep
	// on the create path is a size bound, not the expiry mechanism, so a row
	// that outlives its expiry until the next sweep is still refused.
	errCodeExpired = errors.New("device code expired")
	// errCodeNotPending means the row was already approved, denied, or marked
	// expired. It is what stops a device code being approved twice, including
	// by a second account racing the first.
	errCodeNotPending = errors.New("device code is no longer pending")
)

// pendingCode is the subset of a device_codes row the approval page needs.
type pendingCode struct {
	UserCode         string
	MachineName      string
	MachinePublicURL string
	ExpiresAt        time.Time
}

// createDeviceCode inserts a fresh pending device code for the machine
// described by name and publicURL, returning the plaintext device code (which
// is not stored: only its hash is) and the stored user code.
func (s *Service) createDeviceCode(ctx context.Context, name, publicURL string, now time.Time) (deviceCode, userCode string, err error) {
	deviceCode, err = newDeviceCode()
	if err != nil {
		return "", "", err
	}

	s.sweepExpired(ctx, now)

	// user_code is UNIQUE, so a collision with a code that is still in the
	// table is possible, if unlikely. Redraw rather than failing the request,
	// but only for that one error: retrying a SQLITE_BUSY or a broken schema
	// four more times just wastes inserts and then reports the last failure
	// instead of the first.
	for attempt := 0; attempt < maxCodeGenerationAttempts; attempt++ {
		userCode, err = newUserCode()
		if err != nil {
			return "", "", err
		}
		_, err = s.db.ExecContext(ctx,
			`INSERT INTO device_codes
			   (device_code, user_code, status, created_at, expires_at, machine_name, machine_public_url)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			hashCode(deviceCode), userCode, statusPending, now, now.Add(deviceCodeTTL), name, publicURL,
		)
		if err == nil {
			return deviceCode, userCode, nil
		}
		if !isUserCodeCollision(err) {
			return "", "", fmt.Errorf("insert device code: %w", err)
		}
	}
	return "", "", fmt.Errorf("insert device code: %w", err)
}

// isUserCodeCollision reports whether err is the UNIQUE violation on
// device_codes.user_code, the one insert failure worth redrawing the code for.
//
// It matches on the constraint text rather than a driver error code so this
// package does not have to import the SQLite driver to name one integer. The
// column name is in the message, so this cannot be confused with a UNIQUE
// violation on device_code or on another table.
func isUserCodeCollision(err error) bool {
	return err != nil &&
		strings.Contains(err.Error(), "UNIQUE constraint failed") &&
		strings.Contains(err.Error(), "device_codes.user_code")
}

// sweepExpired deletes device_codes rows that are past their expiry. It runs
// on the create path because that is the only unauthenticated write here, so
// it is where the table grows, and one DELETE over an indexed comparison is
// cheaper than the row it is about to insert.
//
// An expired row carries nothing: every read path already rejects one, so
// deleting it changes no answer this service gives, and the machines row an
// approval created lives in a separate table the sweep does not touch. A
// failure is logged and swallowed, because a sweep that did not run must not
// fail the request that triggered it.
func (s *Service) sweepExpired(ctx context.Context, now time.Time) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM device_codes WHERE expires_at < ?`, now); err != nil {
		log.Printf("device: sweep expired device codes: %v", err)
	}
}

// lookupPending returns the pending, unexpired row for a typed user code, so
// the approval page can show the operator which machine they are about to
// bind. It reports errCodeExpired and errCodeNotPending distinctly, because
// "that code has expired, run setup-vm again" and "that code was already
// used" are different things to tell a human.
func (s *Service) lookupPending(ctx context.Context, userCode string, now time.Time) (pendingCode, error) {
	var (
		pc        pendingCode
		status    string
		expiresAt time.Time
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT user_code, status, expires_at, machine_name, machine_public_url
		   FROM device_codes WHERE user_code = ?`, userCode,
	).Scan(&pc.UserCode, &status, &expiresAt, &pc.MachineName, &pc.MachinePublicURL)
	if errors.Is(err, sql.ErrNoRows) {
		return pendingCode{}, errCodeNotFound
	}
	if err != nil {
		return pendingCode{}, fmt.Errorf("look up user code: %w", err)
	}
	pc.ExpiresAt = expiresAt

	if status != statusPending {
		return pendingCode{}, errCodeNotPending
	}
	if !now.Before(expiresAt) {
		s.markExpired(ctx, userCode)
		return pendingCode{}, errCodeExpired
	}
	return pc, nil
}

// markExpired flips a pending row to 'expired'. It is bookkeeping only: every
// read already compares expires_at, so a failure here cannot make an expired
// code usable, which is why it does not propagate an error.
func (s *Service) markExpired(ctx context.Context, userCode string) {
	_, _ = s.db.ExecContext(ctx,
		`UPDATE device_codes SET status = ? WHERE user_code = ? AND status = ?`,
		statusExpired, userCode, statusPending)
}

// approve binds a pending device code to accountID, registering the machine
// it describes in the same transaction, and returns the machine id.
//
// The returned id is machines.id. It is the value the polling client writes
// into machine.json and the value that goes into `aud` on every access token
// minted for this machine; it is never the hostname or the public URL.
func (s *Service) approve(ctx context.Context, userCode, accountID string, now time.Time) (machineID string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin approval tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		status     string
		expiresAt  time.Time
		name       string
		publicURL  string
		rowAccount sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT status, expires_at, machine_name, machine_public_url, account_id
		   FROM device_codes WHERE user_code = ?`, userCode,
	).Scan(&status, &expiresAt, &name, &publicURL, &rowAccount)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errCodeNotFound
	}
	if err != nil {
		return "", fmt.Errorf("look up user code for approval: %w", err)
	}
	if status != statusPending {
		return "", errCodeNotPending
	}
	if !now.Before(expiresAt) {
		return "", errCodeExpired
	}

	machineID, err = registerMachine(ctx, tx, accountID, name, publicURL, now)
	if err != nil {
		return "", err
	}

	// The status guard is what makes a second approval lose rather than
	// overwrite the first: two operators (or two tabs) racing the same code
	// both pass the SELECT above, and exactly one of them updates a row.
	res, err := tx.ExecContext(ctx,
		`UPDATE device_codes
		    SET status = ?, account_id = ?, approved_at = ?, machine_id = ?
		  WHERE user_code = ? AND status = ?`,
		statusApproved, accountID, now, machineID, userCode, statusPending)
	if err != nil {
		return "", fmt.Errorf("approve device code: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", errCodeNotPending
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit approval: %w", err)
	}
	return machineID, nil
}

// deny marks a pending device code denied, so the polling client is told
// access_denied instead of waiting out the expiry.
func (s *Service) deny(ctx context.Context, userCode string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE device_codes SET status = ? WHERE user_code = ? AND status = ?`,
		statusDenied, userCode, statusPending)
	if err != nil {
		return fmt.Errorf("deny device code: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errCodeNotPending
	}
	return nil
}

// registerMachine returns the id of the machines row for this account and
// public URL, inserting one if there is none.
//
// It reuses an existing, unrevoked row rather than always inserting, because
// `ao setup-vm` is documented as idempotent and re-runnable: a re-bind of the
// same VM must keep the same machine id, or every access token already minted
// for it stops matching the id in machine.json and the desktop's machine list
// fills with duplicates of one box.
func registerMachine(ctx context.Context, tx *sql.Tx, accountID, name, publicURL string, now time.Time) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM machines
		  WHERE account_id = ? AND hostname = ? AND revoked_at IS NULL`,
		accountID, publicURL,
	).Scan(&id)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx, `UPDATE machines SET name = ? WHERE id = ?`, name, id); err != nil {
			return "", fmt.Errorf("update machine name: %w", err)
		}
		return id, nil
	case errors.Is(err, sql.ErrNoRows):
		id = uuid.NewString()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO machines (id, account_id, name, hostname, created_at) VALUES (?, ?, ?, ?, ?)`,
			id, accountID, name, publicURL, now,
		); err != nil {
			return "", fmt.Errorf("insert machine: %w", err)
		}
		return id, nil
	default:
		return "", fmt.Errorf("look up existing machine: %w", err)
	}
}

// grant is what a successful poll returns: the triple `ao setup-vm` writes
// into machine.json.
type grant struct {
	MachineID string
	AccountID string
	PublicURL string
}

// pollResult reports what the polling client should be told for one device
// code. Exactly one of grant or errCode is set.
type pollResult struct {
	grant   *grant
	errCode string
}

// poll implements the device access token request's server side: the interval
// enforcement, the expiry check, and the state read, in one transaction so two
// concurrent polls of the same code cannot both pass the interval gate.
func (s *Service) poll(ctx context.Context, deviceCode string, now time.Time) (pollResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return pollResult{}, fmt.Errorf("begin poll tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		userCode     string
		status       string
		expiresAt    time.Time
		lastPolledAt sql.NullTime
		accountID    sql.NullString
		machineID    sql.NullString
		publicURL    string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT user_code, status, expires_at, last_polled_at, account_id, machine_id, machine_public_url
		   FROM device_codes WHERE device_code = ?`, hashCode(deviceCode),
	).Scan(&userCode, &status, &expiresAt, &lastPolledAt, &accountID, &machineID, &publicURL)
	if errors.Is(err, sql.ErrNoRows) {
		// RFC 8628 sends an unknown device code through the OAuth
		// invalid_grant error, the same as a code that was never issued.
		return pollResult{errCode: errInvalidGrant}, nil
	}
	if err != nil {
		return pollResult{}, fmt.Errorf("look up device code: %w", err)
	}

	// Expiry is checked before the interval so that a client hammering a dead
	// code is told to stop rather than being told to slow down forever.
	if !now.Before(expiresAt) {
		if status == statusPending {
			if _, err := tx.ExecContext(ctx,
				`UPDATE device_codes SET status = ? WHERE user_code = ? AND status = ?`,
				statusExpired, userCode, statusPending); err != nil {
				return pollResult{}, fmt.Errorf("expire device code: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return pollResult{}, fmt.Errorf("commit expiry: %w", err)
			}
		}
		return pollResult{errCode: errExpiredToken}, nil
	}

	// last_polled_at is deliberately not advanced on a slow_down, so a client
	// that ignores the interval is not locked out permanently: it still gets
	// through one poll per interval, which is exactly the rate the RFC's
	// interval describes.
	if lastPolledAt.Valid && now.Sub(lastPolledAt.Time) < pollInterval {
		return pollResult{errCode: errSlowDown}, nil
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE device_codes SET last_polled_at = ? WHERE user_code = ?`, now, userCode,
	); err != nil {
		return pollResult{}, fmt.Errorf("record poll: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return pollResult{}, fmt.Errorf("commit poll: %w", err)
	}

	switch status {
	case statusPending:
		return pollResult{errCode: errAuthorizationPending}, nil
	case statusDenied:
		return pollResult{errCode: errAccessDenied}, nil
	case statusApproved:
		if !accountID.Valid || !machineID.Valid {
			return pollResult{}, fmt.Errorf("device code %q is approved with no account or machine", userCode)
		}
		return pollResult{grant: &grant{
			MachineID: machineID.String,
			AccountID: accountID.String,
			PublicURL: publicURL,
		}}, nil
	default:
		return pollResult{errCode: errExpiredToken}, nil
	}
}

// Machine is one row of the list-machines response and the account home page.
type Machine struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	PublicURL string     `json:"public_url"`
	CreatedAt time.Time  `json:"created_at"`
	LastSeen  *time.Time `json:"last_seen"`
}

// ListMachines returns the account's unrevoked machines, newest first. A
// revoked machine is omitted rather than flagged: the desktop and the account
// home page must not offer it, and nothing in the current UI has a use for its
// tombstone.
func (s *Service) ListMachines(ctx context.Context, accountID string) ([]Machine, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, hostname, created_at, last_seen
		   FROM machines
		  WHERE account_id = ? AND revoked_at IS NULL
		  ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list machines: %w", err)
	}
	defer rows.Close()

	// Non-nil so an account with no machines serializes as [] rather than
	// null, which a JSON client would otherwise have to special-case.
	out := []Machine{}
	for rows.Next() {
		var (
			m        Machine
			lastSeen sql.NullTime
		)
		if err := rows.Scan(&m.ID, &m.Name, &m.PublicURL, &m.CreatedAt, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan machine: %w", err)
		}
		if lastSeen.Valid {
			t := lastSeen.Time
			m.LastSeen = &t
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate machines: %w", err)
	}
	return out, nil
}

// RevokeMachine sets revoked_at on one of accountID's live machines.
//
// A revoked machine no longer appears in the list, cannot mint a machine
// token, and cannot re-bind under the same machines.id until a new bind
// inserts a fresh row. Returns false when no live row matched (missing,
// foreign, or already revoked), without distinguishing those cases.
func (s *Service) RevokeMachine(ctx context.Context, accountID, machineID string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE machines SET revoked_at = ?
		  WHERE id = ? AND account_id = ? AND revoked_at IS NULL`,
		now, machineID, accountID,
	)
	if err != nil {
		return false, fmt.Errorf("revoke machine %q: %w", machineID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("revoke machine %q rows affected: %w", machineID, err)
	}
	return n > 0, nil
}
