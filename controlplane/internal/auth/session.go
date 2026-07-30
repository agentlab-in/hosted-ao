package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookieName = "ao_session"

	// sessionTTL is deliberately long: this cookie only carries the operator
	// through the browser pages the control plane serves (sign-in today,
	// device approval once a later task adds it). It is unrelated to the
	// short-lived access tokens the keys-and-tokens work issues for
	// machine-facing API calls.
	sessionTTL = 30 * 24 * time.Hour

	sessionKeyFile = "session_key"
	sessionKeyLen  = 32
)

// loadOrCreateSessionKey reads the HMAC key used to sign session and OAuth
// flow cookies from dataDir, generating and persisting one on first boot.
// Losing this file just signs everyone out; it is not the EdDSA signing key
// the keys-and-tokens work manages separately.
func loadOrCreateSessionKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, sessionKeyFile)

	if b, err := os.ReadFile(path); err == nil {
		if len(b) != sessionKeyLen {
			return nil, fmt.Errorf("session key at %s has wrong length %d, want %d", path, len(b), sessionKeyLen)
		}
		return b, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read session key: %w", err)
	}

	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	key := make([]byte, sessionKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate session key: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write session key: %w", err)
	}
	return key, nil
}

// sign returns the HMAC-SHA256 of payload under the service's session key.
func (s *Service) sign(payload string) []byte {
	mac := hmac.New(sha256.New, s.sessionKey)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// signedCookieValue packages payload with its HMAC signature into a cookie
// value: base64url(payload) + "." + base64url(signature).
func (s *Service) signedCookieValue(payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(s.sign(payload))
}

// verifySignedCookieValue reverses signedCookieValue, returning the payload
// only if the signature matches.
func (s *Service) verifySignedCookieValue(value string) (string, bool) {
	encPayload, encSig, ok := strings.Cut(value, ".")
	if !ok {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		return "", false
	}
	sig, err := base64.RawURLEncoding.DecodeString(encSig)
	if err != nil {
		return "", false
	}
	if !hmac.Equal(sig, s.sign(string(payload))) {
		return "", false
	}
	return string(payload), true
}

// issueSession sets the signed session cookie identifying accountID as the
// signed-in operator.
func (s *Service) issueSession(w http.ResponseWriter, accountID string) {
	exp := time.Now().Add(sessionTTL)
	payload := accountID + "|" + strconv.FormatInt(exp.Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.signedCookieValue(payload),
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

// AccountFromRequest returns the signed-in account id from the session
// cookie, if the request carries a valid, unexpired one. Later pages (the
// device-approval flow) use this to identify the operator.
func (s *Service) AccountFromRequest(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}
	payload, ok := s.verifySignedCookieValue(c.Value)
	if !ok {
		return "", false
	}
	accountID, expRaw, ok := strings.Cut(payload, "|")
	if !ok {
		return "", false
	}
	exp, err := strconv.ParseInt(expRaw, 10, 64)
	if err != nil {
		return "", false
	}
	if time.Now().Unix() > exp {
		return "", false
	}
	return accountID, true
}
