package provider

import (
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/varcli/cpa-plugins/plugins/kiro/kironx"
)

type browserCallbackManagementRequest struct {
	RedirectURL string `json:"redirect_url"`
}

type browserCallbackOutcome struct {
	Status      string
	State       string
	URL         string
	UserCode    string
	ExpiresAt   time.Time
	LoginOption string
	OAuthError  bool
}

func handleBrowserCallbackManagement(req managementRequest) ([]byte, error) {
	if !strings.EqualFold(req.Method, http.MethodPost) {
		return okEnvelope(managementResponse{
			StatusCode: http.StatusMethodNotAllowed,
			Headers:    jsonHeaders(),
			Body:       mustJSON(map[string]any{"error": "method_not_allowed"}),
		})
	}
	var body browserCallbackManagementRequest
	if errDecode := json.Unmarshal(req.Body, &body); errDecode != nil {
		return browserCallbackError(http.StatusBadRequest, "invalid_callback", "Kiro callback request must contain JSON")
	}
	callbackURL, errParse := url.Parse(strings.TrimSpace(body.RedirectURL))
	if errParse != nil || callbackURL.Scheme == "" || callbackURL.Host == "" {
		return browserCallbackError(http.StatusBadRequest, "invalid_callback", "Kiro callback URL is invalid")
	}
	outcome, errCallback := processBrowserCallback(callbackURL, req.HostCallbackID)
	if errCallback != nil {
		return browserCallbackErrorResponse(errCallback)
	}
	bodyResponse := map[string]any{"status": outcome.Status, "state": outcome.State}
	if outcome.URL != "" {
		bodyResponse["url"] = outcome.URL
		bodyResponse["user_code"] = outcome.UserCode
		bodyResponse["expires_at"] = outcome.ExpiresAt.Format(time.RFC3339)
		bodyResponse["login_option"] = outcome.LoginOption
	}
	return okEnvelope(managementResponse{
		StatusCode: http.StatusOK,
		Headers:    jsonHeaders(),
		Body:       mustJSON(bodyResponse),
	})
}

// pastedCallbackStatus tells a caller what consumePastedCallback did.
type pastedCallbackStatus int

const (
	// pastedCallbackAbsent: the request body carried no callback_url.
	pastedCallbackAbsent pastedCallbackStatus = iota
	// pastedCallbackAccepted: the callback was stored; the caller should carry
	// on with its normal poll to finish the login and persist the credential.
	pastedCallbackAccepted
	// pastedCallbackDone: the callback could not be accepted (or needs another
	// step); the returned response is final.
	pastedCallbackDone
)

// consumePastedCallback handles a callback URL pasted into the panel.
//
// v0.5.2: this used to be a metadata copy — the panel POSTed callback_url,
// the handler wrote it into session metadata and returned 200, and nothing in
// the repo ever read that key. The pasted authorization code was silently
// discarded: pollBrowserLoginRequest only consults the in-memory session
// callback (filled by storeBrowserCallback) and the on-disk .oauth file, so the
// login sat at "pending" forever and no credential was ever produced.
//
// Routing the pasted URL through processBrowserCallback reuses the exact
// validation + persistence path the resource route takes, so both entry points
// behave identically.
func consumePastedCallback(req managementRequest) ([]byte, pastedCallbackStatus) {
	var input struct {
		CallbackURL string `json:"callback_url"`
	}
	if len(req.Body) == 0 || json.Unmarshal(req.Body, &input) != nil || strings.TrimSpace(input.CallbackURL) == "" {
		return nil, pastedCallbackAbsent
	}
	callbackURL, errParse := url.Parse(strings.TrimSpace(input.CallbackURL))
	if errParse != nil || callbackURL.Scheme == "" || callbackURL.Host == "" {
		return managementJSON(http.StatusBadRequest, map[string]any{"error": "invalid_callback"}), pastedCallbackDone
	}
	outcome, errCallback := processBrowserCallback(callbackURL, req.HostCallbackID)
	if errCallback != nil {
		return browserCallbackBody(errCallback), pastedCallbackDone
	}
	if outcome.OAuthError {
		return managementJSON(http.StatusBadRequest, map[string]any{
			"error": "oauth_error", "message": "Kiro authorization failed: " + outcome.State,
		}), pastedCallbackDone
	}
	if outcome.Status == "continue" {
		// 组织/Builder ID 登录：回调只完成第一步，用户还需访问验证地址。
		// 资源路由走 302 跳转，这里没有浏览器可跳，必须把地址回给面板。
		return managementJSON(http.StatusOK, map[string]any{
			"status":     "continue",
			"state":      outcome.State,
			"url":        outcome.URL,
			"user_code":  outcome.UserCode,
			"expires_at": outcome.ExpiresAt.Format(time.RFC3339),
		}), pastedCallbackDone
	}
	return nil, pastedCallbackAccepted
}

func handleBrowserCallbackResource(req managementRequest) ([]byte, error) {
	if !strings.EqualFold(req.Method, http.MethodGet) {
		return resourceCallbackPage(http.StatusMethodNotAllowed, "Unsupported request", "This Kiro callback only accepts GET requests.")
	}
	callbackURL, errURL := browserResourceCallbackURL(req)
	if errURL != nil {
		return resourceCallbackError(errURL)
	}
	outcome, errCallback := processBrowserCallback(callbackURL, req.HostCallbackID)
	if errCallback != nil {
		return resourceCallbackError(errCallback)
	}
	if outcome.Status == "continue" {
		return okEnvelope(managementResponse{
			StatusCode: http.StatusFound,
			Headers: http.Header{
				"Cache-Control":   []string{"no-store"},
				"Location":        []string{outcome.URL},
				"Referrer-Policy": []string{"no-referrer"},
			},
		})
	}
	if outcome.OAuthError {
		return resourceCallbackPage(http.StatusBadRequest, "Kiro authorization failed", "Return to CPA and start the Kiro login again.")
	}
	return resourceCallbackPage(http.StatusOK, "Kiro authorization received", "You can close this page and return to CPA.")
}

func processBrowserCallback(callbackURL *url.URL, callbackID string) (browserCallbackOutcome, error) {
	var outcome browserCallbackOutcome
	query := callbackURL.Query()
	state := strings.TrimSpace(query.Get("state"))
	if state == "" {
		return outcome, callbackStatusError(http.StatusBadRequest, "missing_state", "Kiro callback URL has no state")
	}
	session, exists := browserLoginSessionForState(state)
	if !exists || session.LoginState.State != state {
		return outcome, callbackStatusError(http.StatusBadRequest, "unknown_state", "Kiro login state is unknown or expired; start login again")
	}
	expiresAt, errExpiry := time.Parse(time.RFC3339, session.LoginState.ExpiresAt)
	if errExpiry != nil || !time.Now().UTC().Before(expiresAt) {
		clearBrowserLoginSession(state)
		return outcome, callbackStatusError(http.StatusBadRequest, "expired_state", "Kiro login state has expired; start login again")
	}
	if !callbackMatchesRedirect(callbackURL, session.LoginState.RedirectURI) {
		return outcome, callbackStatusError(http.StatusBadRequest, "invalid_callback", "Kiro callback URL does not match the login redirect URI")
	}
	callback := oauthCallbackPayload{
		Code: strings.TrimSpace(query.Get("code")), State: state, Error: strings.TrimSpace(query.Get("error")),
	}
	if callback.Code != "" || callback.Error != "" {
		if !storeBrowserCallback(state, callback) {
			return outcome, callbackStatusError(http.StatusConflict, "login_already_continued", "Kiro login has already continued; return to CPA")
		}
		return browserCallbackOutcome{Status: "accepted", State: state, OAuthError: callback.Error != ""}, nil
	}

	loginOption := normalizeKiroLoginOption(query.Get("login_option"))
	if loginOption == "" {
		loginOption = normalizeKiroLoginOption(query.Get("loginOption"))
	}
	if loginOption == "" {
		return outcome, callbackStatusError(http.StatusBadRequest, "missing_login_option", "Kiro callback has neither an OAuth code nor a supported login option")
	}
	if !claimBrowserDeviceContinuation(state) {
		return outcome, callbackStatusError(http.StatusConflict, "login_already_continued", "Kiro organization login has already continued; open the current verification link")
	}
	claimed := true
	defer func() {
		if claimed {
			releaseBrowserDeviceContinuation(state)
		}
	}()
	startURL := strings.TrimSpace(query.Get("issuer_url"))
	if startURL == "" {
		startURL = strings.TrimSpace(query.Get("issuerUrl"))
	}
	if startURL == "" && loginOption == "builderid" {
		startURL = defaultSSOStartURL
	}
	region := strings.TrimSpace(query.Get("idc_region"))
	if region == "" {
		region = strings.TrimSpace(query.Get("idcRegion"))
	}
	if region == "" {
		region = strings.TrimSpace(loadedConfig().SSORegion)
	}
	if region == "" {
		region = defaultRegion
	}
	apiRegion := kironx.NonEmpty(strings.TrimSpace(session.LoginState.APIRegion), configuredAPIRegion(loadedConfig()))
	deviceState, verificationURL, expiresAt, errDevice := beginDeviceAuthorization(callbackID, state, startURL, region, apiRegion)
	if errDevice != nil {
		return outcome, errDevice
	}
	if !storeBrowserDeviceContinuation(state, deviceState) {
		clearDeviceLoginPoll(state)
		return outcome, callbackStatusError(http.StatusBadRequest, "unknown_state", "Kiro login state is unknown or expired; start login again")
	}
	claimed = false
	return browserCallbackOutcome{
		Status: "continue", State: state, URL: verificationURL, UserCode: deviceState.UserCode,
		ExpiresAt: expiresAt, LoginOption: loginOption,
	}, nil
}

func browserResourceCallbackURL(req managementRequest) (*url.URL, error) {
	expected, errParse := parseBrowserRedirectURI(loadedConfig().BrowserRedirectURI)
	if errParse != nil {
		return nil, callbackStatusError(http.StatusInternalServerError, "invalid_login_config", "Kiro browser redirect URI is invalid")
	}
	path := strings.TrimRight(strings.TrimSpace(req.Path), "/")
	basePath := strings.TrimRight(expected.Path, "/")
	if path != basePath && path != basePath+"/signin/callback" && path != basePath+"/oauth/callback" {
		return nil, callbackStatusError(http.StatusBadRequest, "invalid_callback", "Kiro callback path does not match the configured redirect URI")
	}
	callback := *expected
	callback.Path = path
	callback.RawQuery = url.Values(req.Query).Encode()
	callback.Fragment = ""
	return &callback, nil
}

func validateVerificationURL(raw, region, startURL string) error {
	parsed, errParse := url.Parse(strings.TrimSpace(raw))
	if errParse != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() == "" {
		return callbackStatusError(http.StatusBadGateway, "invalid_login_response", "Kiro device authorization returned an unsafe verification URL")
	}
	expectedHost := "device.sso." + strings.ToLower(strings.TrimSpace(region)) + ".amazonaws.com"
	if strings.EqualFold(parsed.Hostname(), expectedHost) {
		return nil
	}
	issuer, errIssuer := url.Parse(strings.TrimSpace(startURL))
	if errIssuer == nil && issuer.Scheme == "https" && issuer.User == nil && strings.EqualFold(parsed.Host, issuer.Host) {
		return nil
	}
	return callbackStatusError(http.StatusBadGateway, "invalid_login_response", "Kiro device authorization returned an untrusted verification URL")
}

func callbackMatchesRedirect(callbackURL *url.URL, redirectURI string) bool {
	expected, errParse := parseBrowserRedirectURI(redirectURI)
	if errParse != nil || callbackURL == nil || callbackURL.User != nil {
		return false
	}
	path := strings.TrimRight(callbackURL.Path, "/")
	return strings.EqualFold(callbackURL.Scheme, expected.Scheme) &&
		strings.EqualFold(callbackURL.Host, expected.Host) &&
		(path == expected.Path || path == expected.Path+"/signin/callback" || path == expected.Path+"/oauth/callback")
}

func parseBrowserRedirectURI(raw string) (*url.URL, error) {
	parsed, errParse := url.Parse(strings.TrimSpace(raw))
	if errParse != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, callbackStatusError(http.StatusBadRequest, "invalid_callback", "Kiro browser redirect URI is invalid")
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		host := strings.ToLower(parsed.Hostname())
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return nil, callbackStatusError(http.StatusBadRequest, "invalid_callback", "Remote Kiro browser redirect URI must use HTTPS")
		}
	default:
		return nil, callbackStatusError(http.StatusBadRequest, "invalid_callback", "Kiro browser redirect URI must use HTTP or HTTPS")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed, nil
}

func normalizeKiroLoginOption(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "awsidc", "aws-idc", "idc", "sso", "organization", "organisation", "enterprise", "iam", "iam-identity-center":
		return "awsidc"
	case "builderid", "builder-id", "builder_id", "internal":
		return "builderid"
	default:
		return ""
	}
}

func browserCallbackError(status int, code, message string) ([]byte, error) {
	return okEnvelope(managementResponse{
		StatusCode: status,
		Headers:    jsonHeaders(),
		Body:       mustJSON(map[string]any{"error": code, "message": message}),
	})
}

// browserCallbackBody renders the envelope for a failed callback. It is split
// out from browserCallbackErrorResponse so callers that already have a
// (body, status) shape of their own can reuse the same mapping.
func browserCallbackBody(err error) []byte {
	status := pluginHTTPStatus(err)
	if status == 0 {
		status = http.StatusInternalServerError
	}
	code := "callback_failed"
	message := "Kiro callback failed"
	if typed, ok := err.(statusError); ok {
		code = typed.Code
		message = typed.Error()
	}
	body, _ := browserCallbackError(status, code, message)
	return body
}

func browserCallbackErrorResponse(err error) ([]byte, error) {
	return browserCallbackBody(err), nil
}

func resourceCallbackError(err error) ([]byte, error) {
	status := pluginHTTPStatus(err)
	if status == 0 {
		status = http.StatusInternalServerError
	}
	message := "Return to CPA and start the Kiro login again."
	if status >= 500 {
		message = "Kiro could not continue the login. Return to CPA and try again."
	}
	return resourceCallbackPage(status, "Kiro authorization failed", message)
}

func resourceCallbackPage(status int, title, message string) ([]byte, error) {
	body := "<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>" + html.EscapeString(title) + "</title><body><main><h1>" + html.EscapeString(title) + "</h1><p>" + html.EscapeString(message) + "</p></main></body></html>"
	return okEnvelope(managementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Cache-Control":           []string{"no-store"},
			"Content-Security-Policy": []string{"default-src 'none'; style-src 'unsafe-inline'"},
			"Content-Type":            []string{"text/html; charset=utf-8"},
			"Referrer-Policy":         []string{"no-referrer"},
			"X-Content-Type-Options":  []string{"nosniff"},
		},
		Body: []byte(body),
	})
}

func callbackStatusError(status int, code, message string) statusError {
	return statusError{Code: code, Message: message, HTTPStatus: status}
}
