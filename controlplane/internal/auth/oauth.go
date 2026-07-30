package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	flowCookieName = "ao_oauth_flow"
	flowTTL        = 10 * time.Minute

	googleScopes = "openid email profile"
)

// googleEndpoints are the three Google URLs the exchange needs. They are a
// field on Service, not package-level constants, so tests can point them at
// an httptest server instead of the real Google.
type googleEndpoints struct {
	AuthURL     string
	TokenURL    string
	UserinfoURL string
}

var defaultGoogleEndpoints = googleEndpoints{
	AuthURL:     "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL:    "https://oauth2.googleapis.com/token",
	UserinfoURL: "https://openidconnect.googleapis.com/v1/userinfo",
}

// flowState is the data carried across the redirect to Google and back,
// packed into the signed ao_oauth_flow cookie: the CSRF state, the PKCE code
// verifier, and where to send the operator once signed in.
type flowState struct {
	State    string
	Verifier string
	Next     string
}

// generatePKCE returns a random RFC 7636 code verifier and its S256
// challenge.
func generatePKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate code verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// generateState returns a random CSRF state value for the authorization
// request.
func generateState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// sanitizeNext restricts a caller-supplied redirect target to a same-site
// relative path, so the login flow can't be used as an open redirect.
func sanitizeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

func (s *Service) setFlowCookie(w http.ResponseWriter, f flowState) {
	exp := time.Now().Add(flowTTL)
	payload := strings.Join([]string{f.State, f.Verifier, f.Next, strconv.FormatInt(exp.Unix(), 10)}, "|")
	http.SetCookie(w, &http.Cookie{
		Name:     flowCookieName,
		Value:    s.signedCookieValue(payload),
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Service) clearFlowCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     flowCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

// flowFromRequest recovers the flow state set by handleGoogleLogin, if the
// callback still carries a valid, unexpired ao_oauth_flow cookie.
func (s *Service) flowFromRequest(r *http.Request) (flowState, bool) {
	c, err := r.Cookie(flowCookieName)
	if err != nil {
		return flowState{}, false
	}
	payload, ok := s.verifySignedCookieValue(c.Value)
	if !ok {
		return flowState{}, false
	}
	parts := strings.Split(payload, "|")
	if len(parts) != 4 {
		return flowState{}, false
	}
	exp, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return flowState{}, false
	}
	if time.Now().Unix() > exp {
		return flowState{}, false
	}
	return flowState{State: parts[0], Verifier: parts[1], Next: parts[2]}, true
}

func (s *Service) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	next := sanitizeNext(r.URL.Query().Get("next"))

	verifier, challenge, err := generatePKCE()
	if err != nil {
		log.Printf("auth: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state, err := generateState()
	if err != nil {
		log.Printf("auth: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setFlowCookie(w, flowState{State: state, Verifier: verifier, Next: next})

	u, err := url.Parse(s.endpoints.AuthURL)
	if err != nil {
		log.Printf("auth: parse google auth url: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	q := u.Query()
	q.Set("client_id", s.clientID)
	q.Set("redirect_uri", s.redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", googleScopes)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()

	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Service) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		s.clearFlowCookie(w)
		http.Redirect(w, r, "/login?error=access_denied", http.StatusFound)
		return
	}

	flow, ok := s.flowFromRequest(r)
	s.clearFlowCookie(w)
	if !ok {
		http.Redirect(w, r, "/login?error=expired", http.StatusFound)
		return
	}
	if state := r.URL.Query().Get("state"); state == "" || state != flow.State {
		http.Redirect(w, r, "/login?error=state_mismatch", http.StatusFound)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/login?error=missing_code", http.StatusFound)
		return
	}

	tok, err := s.exchangeCode(r.Context(), code, flow.Verifier)
	if err != nil {
		log.Printf("auth: google token exchange failed: %v", err)
		http.Redirect(w, r, "/login?error=exchange_failed", http.StatusFound)
		return
	}

	profile, err := s.fetchUserinfo(r.Context(), tok.AccessToken)
	if err != nil {
		log.Printf("auth: google userinfo fetch failed: %v", err)
		http.Redirect(w, r, "/login?error=profile_failed", http.StatusFound)
		return
	}

	accountID, err := s.upsertAccount(r.Context(), profile.Subject, profile.Email)
	if err != nil {
		log.Printf("auth: account upsert failed: %v", err)
		http.Redirect(w, r, "/login?error=internal", http.StatusFound)
		return
	}

	s.issueSession(w, accountID)
	http.Redirect(w, r, flow.Next, http.StatusFound)
}

// tokenResponse is the subset of Google's token endpoint response the
// control plane needs. id_token is not parsed: the access token is used
// directly against the userinfo endpoint instead, so login never has to
// verify a Google-issued JWT signature itself.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
}

func (s *Service) exchangeCode(ctx context.Context, code, verifier string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", s.clientID)
	form.Set("client_secret", s.clientSecret)
	form.Set("redirect_uri", s.redirectURI)
	form.Set("grant_type", "authorization_code")
	form.Set("code_verifier", verifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoints.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, body)
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, errors.New("token response missing access_token")
	}
	return &tok, nil
}

// googleProfile is the subset of the OpenID Connect userinfo response the
// control plane needs to identify the account.
type googleProfile struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
}

func (s *Service) fetchUserinfo(ctx context.Context, accessToken string) (*googleProfile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoints.UserinfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read userinfo response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo endpoint returned %d: %s", resp.StatusCode, body)
	}

	var profile googleProfile
	if err := json.Unmarshal(body, &profile); err != nil {
		return nil, fmt.Errorf("decode userinfo response: %w", err)
	}
	if profile.Subject == "" {
		return nil, errors.New("userinfo response missing sub")
	}
	if profile.Email == "" {
		return nil, errors.New("userinfo response missing email")
	}
	return &profile, nil
}
