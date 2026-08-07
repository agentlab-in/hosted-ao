package device

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/api"
)

// The RFC 8628 section 3.5 error codes, plus the two OAuth ones a device
// access token request can produce.
const (
	errAuthorizationPending = "authorization_pending"
	errSlowDown             = "slow_down"
	errAccessDenied         = "access_denied"
	errExpiredToken         = "expired_token"
	errInvalidGrant         = "invalid_grant"
	errInvalidRequest       = "invalid_request"
	errUnsupportedGrantType = "unsupported_grant_type"
)

// deviceCodeGrantType is the grant type RFC 8628 assigns to the device access
// token request.
const deviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// deviceCodeResponse is the RFC 8628 section 3.2 device authorization
// response, plus nothing. The machine triple comes later, from the token
// endpoint, because until a human approves there is no machine.
type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// bindingResponse is what an approved poll returns: the machine triple
// `ao setup-vm` writes into machine.json, and nothing else.
//
// It carries no access token, which is a deliberate departure from the RFC
// 8628 section 3.5 response shape. The VM does not present tokens, it verifies
// them, so `ao setup-vm` never decoded the one that used to be here; minting it
// created a live 15 minute credential on every bind, sent it across the wire,
// and handed it to a client whose only correct move was to drop it. The desktop
// gets machine tokens from POST /api/v1/machines/{id}/token, against a
// credential of its own. The one client of this endpoint is `ao setup-vm`, so
// nothing generically OAuth reads this body.
//
// machine_id is machines.id, and it is the `aud` of every access token minted
// for this machine. A client writing machine.json must use this field for
// machineId and must not substitute the public URL, or `ao vm serve` will
// reject every token it is ever shown.
type bindingResponse struct {
	MachineID string `json:"machine_id"`
	AccountID string `json:"account_id"`
	PublicURL string `json:"public_url"`
}

// handleDeviceCode is the device authorization endpoint: `ao setup-vm` calls
// it once, prints the user code and verification URI it returns, and then
// polls the token endpoint with the device code.
//
// The machine's name and public URL are supplied here rather than at approval
// because the VM is the only party that knows them, and the approval page has
// to show the operator which box they are about to bind.
func (s *Service) handleDeviceCode(w http.ResponseWriter, r *http.Request) {
	params, err := api.ReadParams(w, r)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, errInvalidRequest, "could not parse the request body")
		return
	}

	publicURL, err := normalizePublicURL(params.Get("public_url"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, errInvalidRequest, err.Error())
		return
	}
	name := strings.TrimSpace(params.Get("machine_name"))
	if name == "" {
		name = hostOf(publicURL)
	}
	// This endpoint is unauthenticated and the row it writes is permanent, so
	// the one attacker-chosen string on it is length checked here rather than
	// left to whatever the body limit happens to be. Runes, not bytes: the name
	// is displayed to a human, and a byte cap would silently give a non-Latin
	// script a quarter of the allowance.
	if len([]rune(name)) > maxMachineNameRunes {
		api.WriteError(w, http.StatusBadRequest, errInvalidRequest,
			fmt.Sprintf("machine_name must be at most %d characters", maxMachineNameRunes))
		return
	}

	now := time.Now().UTC()
	deviceCode, userCode, err := s.createDeviceCode(r.Context(), name, publicURL, now)
	if err != nil {
		log.Printf("device: create device code: %v", err)
		api.WriteError(w, http.StatusInternalServerError, "server_error", "could not issue a device code")
		return
	}

	verificationURI := s.verificationURI()
	api.WriteJSON(w, http.StatusOK, deviceCodeResponse{
		DeviceCode: deviceCode,
		UserCode:   formatUserCode(userCode),
		// verification_uri_complete carries the code in the URL so the
		// operator can follow a link instead of typing, per RFC 8628
		// section 3.3.1. The enter-code page prefills from it but still shows
		// the confirmation step, so a followed link never approves anything
		// on its own.
		VerificationURI:         verificationURI,
		VerificationURIComplete: verificationURI + "?user_code=" + url.QueryEscape(formatUserCode(userCode)),
		ExpiresIn:               int(deviceCodeTTL.Seconds()),
		Interval:                int(pollInterval.Seconds()),
	})
}

// handleDeviceToken is the device access token request endpoint. It returns
// the RFC 8628 error codes while the flow is in progress
// (authorization_pending, slow_down, expired_token, access_denied) and, once
// approved, the machine triple. See bindingResponse for why no token comes
// back with it.
//
// A successful poll is repeatable until the device code expires, rather than
// consuming the code on first success. The device code is already bounded by
// deviceCodeTTL and held only by the VM that generated it, and a dropped
// response on an otherwise successful setup would otherwise force the
// operator back to the browser to approve a second code.
func (s *Service) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	params, err := api.ReadParams(w, r)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, errInvalidRequest, "could not parse the request body")
		return
	}
	if gt := params.Get("grant_type"); gt != "" && gt != deviceCodeGrantType {
		api.WriteError(w, http.StatusBadRequest, errUnsupportedGrantType, "expected grant_type "+deviceCodeGrantType)
		return
	}
	deviceCode := strings.TrimSpace(params.Get("device_code"))
	if deviceCode == "" {
		api.WriteError(w, http.StatusBadRequest, errInvalidRequest, "device_code is required")
		return
	}

	res, err := s.poll(r.Context(), deviceCode, time.Now().UTC())
	if err != nil {
		log.Printf("device: poll: %v", err)
		api.WriteError(w, http.StatusInternalServerError, "server_error", "could not check the device code")
		return
	}
	if res.grant == nil {
		api.WriteError(w, statusForPollError(res.errCode), res.errCode, pollErrorDescription(res.errCode))
		return
	}

	api.WriteJSON(w, http.StatusOK, bindingResponse{
		MachineID: res.grant.MachineID,
		AccountID: res.grant.AccountID,
		PublicURL: res.grant.PublicURL,
	})
}

// statusForPollError maps a device flow error code to its HTTP status.
// authorization_pending and slow_down are 400 per RFC 8628 section 3.5, even
// though they are the normal in-progress states rather than failures.
func statusForPollError(code string) int {
	if code == errAccessDenied {
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}

func pollErrorDescription(code string) string {
	switch code {
	case errAuthorizationPending:
		return "the device code has not been approved yet"
	case errSlowDown:
		return "polling faster than the interval in the device authorization response"
	case errExpiredToken:
		return "the device code has expired, start the flow again"
	case errAccessDenied:
		return "the request was denied in the browser"
	case errInvalidGrant:
		return "unknown device code"
	default:
		return ""
	}
}
