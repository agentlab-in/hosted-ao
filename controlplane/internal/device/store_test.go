package device

import (
	"testing"
	"time"
)

// TestIsUserCodeCollision_TellsTheRedrawableErrorApart uses errors the real
// driver produced, not hand-written ones, because the whole point of the check
// is that it recognises what SQLite actually says. createDeviceCode used to
// treat every insert failure as a collision and burn five redraws on a
// SQLITE_BUSY or a broken schema before reporting only the last one.
func TestIsUserCodeCollision_TellsTheRedrawableErrorApart(t *testing.T) {
	h := newHarness(t)
	now := time.Now().UTC()

	issued := h.requestCode(testPublicURL, "prod vm")
	var userCode string
	if err := h.db.QueryRow(`SELECT user_code FROM device_codes`).Scan(&userCode); err != nil {
		t.Fatalf("read the stored user code: %v", err)
	}
	if userCode == "" || issued.UserCode == "" {
		t.Fatal("no code was issued")
	}

	insert := func(deviceCode, code string, expiresAt any) error {
		_, err := h.db.Exec(
			`INSERT INTO device_codes
			   (device_code, user_code, status, created_at, expires_at, machine_name, machine_public_url)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			deviceCode, code, statusPending, now, expiresAt, "vm", testPublicURL)
		return err
	}

	// The one error worth redrawing for.
	collision := insert(hashCode("another-device-code"), userCode, now.Add(deviceCodeTTL))
	if collision == nil {
		t.Fatal("inserting a duplicate user_code succeeded, want a UNIQUE violation")
	}
	if !isUserCodeCollision(collision) {
		t.Errorf("isUserCodeCollision(%v) = false, want true", collision)
	}

	// A UNIQUE violation on the other unique column is not it: redrawing the
	// user code would never resolve it.
	var storedDeviceCode string
	if err := h.db.QueryRow(`SELECT device_code FROM device_codes`).Scan(&storedDeviceCode); err != nil {
		t.Fatalf("read the stored device code: %v", err)
	}
	dup := insert(storedDeviceCode, "ZZZZZZZZ", now.Add(deviceCodeTTL))
	if dup == nil {
		t.Fatal("inserting a duplicate device_code succeeded, want a UNIQUE violation")
	}
	if isUserCodeCollision(dup) {
		t.Errorf("isUserCodeCollision(%v) = true for a device_code collision, want false", dup)
	}

	// And neither is a constraint failure that has nothing to do with either.
	notNull := insert(hashCode("yet-another"), "YYYYYYYY", nil)
	if notNull == nil {
		t.Fatal("inserting a NULL expires_at succeeded, want a NOT NULL violation")
	}
	if isUserCodeCollision(notNull) {
		t.Errorf("isUserCodeCollision(%v) = true for a NOT NULL violation, want false", notNull)
	}

	if isUserCodeCollision(nil) {
		t.Error("isUserCodeCollision(nil) = true, want false")
	}
}
