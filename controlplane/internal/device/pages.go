package device

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// enterPageData is the html/template data for templates/device_enter.html.
type enterPageData struct {
	UserCode string
	Error    string
}

// confirmPageData is the html/template data for
// templates/device_confirm.html: what the operator is being asked to approve.
type confirmPageData struct {
	UserCode    string
	MachineName string
	PublicURL   string
	ExpiresIn   string
}

// resultPageData is the html/template data for templates/device_result.html.
type resultPageData struct {
	Approved    bool
	MachineName string
	PublicURL   string
}

// requireAccount returns the signed-in account id, or sends the operator
// through sign-in and returns false. next carries the full path and query the
// operator asked for, so a link with ?user_code= in it comes back to a
// prefilled form after Google rather than dead-ending on the sign-in page.
func (s *Service) requireAccount(w http.ResponseWriter, r *http.Request, next string) (string, bool) {
	if accountID, ok := s.sessions.AccountFromRequest(r); ok {
		return accountID, true
	}
	http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusFound)
	return "", false
}

// handleEnterCodePage renders the enter-code form, prefilled from
// ?user_code= when the operator followed verification_uri_complete.
func (s *Service) handleEnterCodePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAccount(w, r, r.URL.RequestURI()); !ok {
		return
	}
	s.renderEnterPage(w, http.StatusOK, enterPageData{
		UserCode: formatUserCode(normalizeUserCode(r.URL.Query().Get("user_code"))),
	})
}

// handleSubmitCode looks up the typed code and, if it is live, renders the
// confirmation page. It never approves: approval is a separate POST, so
// following a verification_uri_complete link cannot bind a machine by itself.
func (s *Service) handleSubmitCode(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.requireAccount(w, r, verificationPath)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderEnterPage(w, http.StatusBadRequest, enterPageData{Error: "That form could not be read. Please try again."})
		return
	}

	typed := r.PostForm.Get("user_code")
	userCode := normalizeUserCode(typed)

	// A user code is short enough to guess given enough tries, so the page is
	// rate limited per signed-in account. The check runs before the lookup so
	// a burst of wrong codes cannot be used to time the database either.
	if !s.attempts.allow(accountID) {
		s.renderEnterPage(w, http.StatusTooManyRequests, enterPageData{
			UserCode: formatUserCode(userCode),
			Error:    "Too many attempts. Wait a minute and try again.",
		})
		return
	}

	if len(userCode) != userCodeLength {
		s.renderEnterPage(w, http.StatusBadRequest, enterPageData{
			UserCode: typed,
			Error:    "That code does not look right. It is eight letters, like WDJB-MJHT.",
		})
		return
	}

	now := time.Now().UTC()
	pc, err := s.lookupPending(r.Context(), userCode, now)
	if err != nil {
		s.renderEnterPage(w, statusForCodeError(err), enterPageData{
			UserCode: formatUserCode(userCode),
			Error:    codeErrorMessage(err),
		})
		return
	}

	s.renderPage(w, http.StatusOK, "device_confirm.html", confirmPageData{
		UserCode:    formatUserCode(pc.UserCode),
		MachineName: pc.MachineName,
		PublicURL:   pc.MachinePublicURL,
		ExpiresIn:   humanizeUntil(pc.ExpiresAt, now),
	})
}

// handleDecision is the approval confirmation: it binds the code to the
// signed-in account and registers the machine, or denies the request.
//
// The session cookie is SameSite=Lax, which browsers do not attach to a
// cross-site POST, so a third-party page cannot drive this form on a
// signed-in operator's behalf.
func (s *Service) handleDecision(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.requireAccount(w, r, verificationPath)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderEnterPage(w, http.StatusBadRequest, enterPageData{Error: "That form could not be read. Please try again."})
		return
	}

	userCode := normalizeUserCode(r.PostForm.Get("user_code"))
	now := time.Now().UTC()

	if r.PostForm.Get("action") == "deny" {
		if err := s.deny(r.Context(), userCode); err != nil && !errors.Is(err, errCodeNotPending) {
			log.Printf("device: deny %q: %v", userCode, err)
		}
		s.renderPage(w, http.StatusOK, "device_result.html", resultPageData{Approved: false})
		return
	}

	// Re-read the row inside approve rather than trusting the hidden field's
	// machine details: the confirmation page is a snapshot, and the code may
	// have expired or been approved elsewhere since it was rendered.
	pc, lookupErr := s.lookupPending(r.Context(), userCode, now)

	machineID, err := s.approve(r.Context(), userCode, accountID, now)
	if err != nil {
		if errors.Is(err, errCodeNotFound) || errors.Is(err, errCodeExpired) || errors.Is(err, errCodeNotPending) {
			s.renderEnterPage(w, statusForCodeError(err), enterPageData{Error: codeErrorMessage(err)})
			return
		}
		log.Printf("device: approve %q: %v", userCode, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if lookupErr != nil {
		// Approval succeeded, so the snapshot read failing is only a display
		// problem; show the result page without the machine details.
		log.Printf("device: approved %s but could not read its machine details: %v", machineID, lookupErr)
	}

	s.renderPage(w, http.StatusOK, "device_result.html", resultPageData{
		Approved:    true,
		MachineName: pc.MachineName,
		PublicURL:   pc.MachinePublicURL,
	})
}

// statusForCodeError maps a lookup failure to the status its page is served
// with, so a wrong code is not a 200.
func statusForCodeError(err error) int {
	switch {
	case errors.Is(err, errCodeNotFound), errors.Is(err, errCodeExpired):
		return http.StatusBadRequest
	case errors.Is(err, errCodeNotPending):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// codeErrorMessage is the operator-facing text for a lookup failure. A code
// that does not exist and a code that expired read differently on purpose:
// only one of them means "run ao setup-vm again".
func codeErrorMessage(err error) string {
	switch {
	case errors.Is(err, errCodeNotFound):
		return "That code was not recognised. Check it and try again."
	case errors.Is(err, errCodeExpired):
		return "That code has expired. Run ao setup-vm again to get a new one."
	case errors.Is(err, errCodeNotPending):
		return "That code has already been used. Run ao setup-vm again to get a new one."
	default:
		return "Something went wrong. Please try again."
	}
}

// humanizeUntil renders the time left on a code as the page's "expires in"
// line. Minutes are enough: the code lives for deviceCodeTTL.
func humanizeUntil(deadline, now time.Time) string {
	remaining := deadline.Sub(now)
	if remaining < time.Minute {
		return "less than a minute"
	}
	minutes := int(remaining.Minutes())
	if minutes == 1 {
		return "1 minute"
	}
	return strconv.Itoa(minutes) + " minutes"
}

func (s *Service) renderEnterPage(w http.ResponseWriter, status int, data enterPageData) {
	s.renderPage(w, status, "device_enter.html", data)
}

func (s *Service) renderPage(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		// The status line is already written, so this can only be logged.
		log.Printf("device: render %s: %v", name, err)
	}
}
