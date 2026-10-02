package provider

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// decodeManagementBody unwraps the managementResponse envelope: CPA's
// managementJSON wraps the real JSON payload in Body, and []byte marshals to
// base64 in the envelope.
func decodeManagementBody(t *testing.T, raw []byte) (status int, payload map[string]any) {
	t.Helper()
	var env envelope
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil || !env.OK {
		t.Fatalf("bad envelope: %s", raw)
	}
	var resp struct {
		StatusCode int    `json:"StatusCode"`
		Body       string `json:"Body"`
	}
	if errDecode := json.Unmarshal(env.Result, &resp); errDecode != nil {
		t.Fatalf("bad management response: %s", env.Result)
	}
	decoded, errB64 := base64.StdEncoding.DecodeString(resp.Body)
	if errB64 != nil {
		t.Fatalf("decode body: %v", errB64)
	}
	if errDecode := json.Unmarshal(decoded, &payload); errDecode != nil {
		t.Fatalf("bad body json: %s", decoded)
	}
	return resp.StatusCode, payload
}

// useBrowserLoginMode pins the plugin config to the kiro-browser flow for one
// test. The default is social-device, whose start step contacts Kiro's device
// authorization endpoint — which these tests do not stub.
func useBrowserLoginMode(t *testing.T) {
	t.Helper()
	original := loadedConfig()
	config := original
	config.LoginMode = browserLoginMode
	configValue.Store(config)
	t.Cleanup(func() { configValue.Store(original) })
}

// seedConsoleOAuthSession registers a full browser login session the way
// startBrowserLoginWithConfig does: the in-memory session (consumed by the
// callback path) plus the console-side metadata (consumed by the poll path).
func seedConsoleOAuthSession(t *testing.T, state string) {
	t.Helper()
	loginState := browserLoginState{
		Version:      1,
		LoginMode:    browserLoginMode,
		State:        state,
		CodeVerifier: "verifier",
		RedirectURI:  "http://localhost:3128",
		TokenURL:     "https://example.invalid/token",
		APIRegion:    defaultRegion,
		ExpiresAt:    time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339),
	}
	storeBrowserLoginSession(loginState)
	t.Cleanup(func() { clearBrowserLoginSession(state) })

	raw, _ := json.Marshal(loginState)
	var metadata map[string]any
	_ = json.Unmarshal(raw, &metadata)
	consoleOAuthSessions.Lock()
	consoleOAuthSessions.metadata[state] = metadata
	consoleOAuthSessions.Unlock()
	t.Cleanup(func() {
		consoleOAuthSessions.Lock()
		delete(consoleOAuthSessions.metadata, state)
		consoleOAuthSessions.Unlock()
	})
}

// v0.5.2 regression: POST /oauth/login/status with a pasted callback_url used
// to only copy the URL into the session metadata and return 200 — nothing ever
// read that key, so the user's authorization code was silently discarded. The
// poll side only consults the in-memory session callback (filled by the
// resource route) and the on-disk .oauth file, so the login stayed pending
// forever and no credential was ever produced.
//
// The test drives the real handler and asserts the pasted code actually lands
// in the login session.
func TestConsoleOAuthStatusConsumesPastedCallback(t *testing.T) {
	const state = "45308d2a-d687-4f44-aa9a-6ced2cf77dc4"
	const code = "7aa6b26a-1a92-42b2-b0f4-f02204c971fd"

	// Seed the login session the way startLogin does. The metadata must carry
	// the full browserLoginState: handleConsoleOAuthStatus hands it to
	// pollLogin, which rejects an incomplete state.
	seedConsoleOAuthSession(t, state)
	t.Cleanup(func() {
		consoleOAuthSessions.Lock()
		delete(consoleOAuthSessions.metadata, state)
		consoleOAuthSessions.Unlock()
	})

	// The token exchange and the credential save both go through the host;
	// stub them so the handler can run to completion.
	originalHTTP, originalCall := hostHTTPDoCall, callHostCall
	hostHTTPDoCall = func(req hostHTTPRequest) (hostHTTPResponse, error) {
		return hostHTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"accessToken":"at","refreshToken":"rt","expiresIn":3600}`),
		}, nil
	}
	callHostCall = func(method string, payload any) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}
	t.Cleanup(func() { hostHTTPDoCall, callHostCall = originalHTTP, originalCall })

	pasted := "http://localhost:3128/oauth/callback?login_option=github&code=" + code + "&state=" + state
	body, _ := json.Marshal(map[string]string{"callback_url": pasted})
	req := managementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/kiro/oauth/login/status",
		Query:  url.Values{"state": []string{state}},
		Body:   body,
	}

	raw, errHandle := handleConsoleOAuthStatus(req)
	if errHandle != nil {
		t.Fatalf("handleConsoleOAuthStatus: %v", errHandle)
	}
	status, out := decodeManagementBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("HTTP %d, body=%v", status, out)
	}
	// The pasted code must have been consumed: the exchange runs and the
	// response reports success rather than sitting at "pending".
	if got, _ := out["status"].(string); got != "success" {
		t.Fatalf("status=%v want success — pasted callback was not consumed (msg=%v)", out["status"], out["message"])
	}
}

// The pasted URL's own state must be honoured, and a bogus one rejected —
// the handler must not blindly trust the session's metadata.
func TestConsoleOAuthStatusRejectsMismatchedPastedState(t *testing.T) {
	const state = "45308d2a-d687-4f44-aa9a-6ced2cf77dc4"
	seedConsoleOAuthSession(t, state)

	pasted := "http://localhost:3128/oauth/callback?code=c&state=00000000-0000-4000-8000-000000000000"
	body, _ := json.Marshal(map[string]string{"callback_url": pasted})
	raw, errHandle := handleConsoleOAuthStatus(managementRequest{
		Method: http.MethodPost, Query: url.Values{"state": []string{state}}, Body: body,
	})
	if errHandle != nil {
		t.Fatalf("handleConsoleOAuthStatus: %v", errHandle)
	}
	status, out := decodeManagementBody(t, raw)
	if status != http.StatusBadRequest {
		t.Fatalf("HTTP %d want 400, body=%v", status, out)
	}
	if got, _ := out["error"].(string); got != "unknown_state" {
		t.Fatalf("a callback whose state does not match the session must be rejected with unknown_state, got %v", out)
	}
}

// Relogin had the same hole as the add-account flow: /oauth/relogin/status is a
// POST endpoint the panel can paste into, and without the shared consume the
// pasted code was dropped and the relogin hung at pending.
func TestReloginStatusConsumesPastedCallback(t *testing.T) {
	const state = "6f0b1c4e-1111-4222-8333-9999aaaabbbb"
	seedConsoleOAuthSession(t, state)

	originalHTTP, originalCall := hostHTTPDoCall, callHostCall
	hostHTTPDoCall = func(req hostHTTPRequest) (hostHTTPResponse, error) {
		return hostHTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"accessToken":"at","refreshToken":"rt","expiresIn":3600}`),
		}, nil
	}
	var savedName string
	callHostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == "host.auth.save" {
			if m, isMap := payload.(map[string]any); isMap {
				savedName, _ = m["name"].(string)
			}
		}
		return json.RawMessage(`{"ok":true}`), nil
	}
	t.Cleanup(func() { hostHTTPDoCall, callHostCall = originalHTTP, originalCall })

	rawState, _ := json.Marshal(browserLoginState{
		Version:      1,
		LoginMode:    browserLoginMode,
		State:        state,
		CodeVerifier: "verifier",
		RedirectURI:  "http://localhost:3128",
		TokenURL:     "https://example.invalid/token",
		APIRegion:    defaultRegion,
		ExpiresAt:    time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339),
	})
	var metadata map[string]any
	_ = json.Unmarshal(rawState, &metadata)
	reloginSessions.Lock()
	reloginSessions.items[state] = reloginSession{
		Metadata: metadata, FileName: "kiro-relogin.json", Existing: map[string]any{"type": "kiro"},
	}
	reloginSessions.Unlock()
	t.Cleanup(func() {
		reloginSessions.Lock()
		delete(reloginSessions.items, state)
		reloginSessions.Unlock()
	})

	pasted := "http://localhost:3128/oauth/callback?code=abc&state=" + state
	body, _ := json.Marshal(map[string]string{"callback_url": pasted})
	raw, errHandle := handleReloginStatus(managementRequest{
		Method: http.MethodPost, Query: url.Values{"state": []string{state}}, Body: body,
	})
	if errHandle != nil {
		t.Fatalf("handleReloginStatus: %v", errHandle)
	}
	status, out := decodeManagementBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("HTTP %d, body=%v", status, out)
	}
	if got, _ := out["status"].(string); got != "success" {
		t.Fatalf("status=%v want success — 重登录粘贴的回调未被消费 (msg=%v)", out["status"], out["message"])
	}
	if savedName != "kiro-relogin.json" {
		t.Fatalf("relogin must replace the existing credential file, saved %q", savedName)
	}
}

// The full panel flow, with no hand-seeded state: start a real login through
// handleConsoleOAuthStart, then paste back the callback the sign-in page would
// have produced. This is the sequence the user actually performs, and it is the
// only test that proves the two halves agree on where the session lives.
//
// The hand-seeded tests above would still pass if starting a login stored its
// state somewhere the callback path never looks; this one would not.
func TestConsoleOAuthPanelFlowStartThenPaste(t *testing.T) {
	useBrowserLoginMode(t)
	// The sign-in URL and token endpoint are never contacted at start time, so
	// only the token exchange and the credential save need stubbing.
	originalHTTP, originalCall := hostHTTPDoCall, callHostCall
	hostHTTPDoCall = func(req hostHTTPRequest) (hostHTTPResponse, error) {
		return hostHTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"accessToken":"at","refreshToken":"rt","expiresIn":3600}`),
		}, nil
	}
	var savedName string
	callHostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == "host.auth.save" {
			if m, isMap := payload.(map[string]any); isMap {
				savedName, _ = m["name"].(string)
			}
		}
		return json.RawMessage(`{"ok":true}`), nil
	}
	t.Cleanup(func() { hostHTTPDoCall, callHostCall = originalHTTP, originalCall })

	// 1. Panel clicks 「新增 Kiro 账号」.
	startRaw, errStart := handleConsoleOAuthStart(managementRequest{
		Method: http.MethodPost, Path: "/v0/management/plugins/kiro/oauth/login/start", Body: []byte(`{}`),
	})
	if errStart != nil {
		t.Fatalf("handleConsoleOAuthStart: %v", errStart)
	}
	startStatus, started := decodeManagementBody(t, startRaw)
	if startStatus != http.StatusOK {
		t.Fatalf("start: HTTP %d body=%v", startStatus, started)
	}
	state, _ := started["state"].(string)
	if strings.TrimSpace(state) == "" {
		t.Fatalf("start returned no state: %v", started)
	}
	t.Cleanup(func() {
		clearBrowserLoginSession(state)
		consoleOAuthSessions.Lock()
		delete(consoleOAuthSessions.metadata, state)
		consoleOAuthSessions.Unlock()
	})

	// 2. The browser lands on the redirect target; the user pastes that URL.
	pasted := defaultRedirectURI + "/oauth/callback?code=panel-flow-code&state=" + url.QueryEscape(state)
	body, _ := json.Marshal(map[string]string{"callback_url": pasted})
	raw, errStatus := handleConsoleOAuthStatus(managementRequest{
		Method: http.MethodPost, Query: url.Values{"state": []string{state}}, Body: body,
	})
	if errStatus != nil {
		t.Fatalf("handleConsoleOAuthStatus: %v", errStatus)
	}
	status, out := decodeManagementBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("paste: HTTP %d body=%v", status, out)
	}
	if got, _ := out["status"].(string); got != "success" {
		t.Fatalf("status=%v want success — 面板流程走完后仍拿不到凭据 (msg=%v)", out["status"], out["message"])
	}
	if savedName == "" {
		t.Fatal("登录成功却没有把凭据写回宿主")
	}
}

// The panel polls every 3s while the user is over in the browser. That poll
// must not destroy the login it is waiting on: the console path carries no auth
// directory, so the on-disk callback probe cannot run, and treating that as a
// terminal error deleted the session before the user could ever paste.
//
// This reproduces the reported symptom: the panel says the session expired
// seconds after it started, and pasting the callback then 404s with
// unknown_state.
func TestConsolePollBeforePasteKeepsSessionAlive(t *testing.T) {
	useBrowserLoginMode(t)
	originalHTTP, originalCall := hostHTTPDoCall, callHostCall
	hostHTTPDoCall = func(req hostHTTPRequest) (hostHTTPResponse, error) {
		return hostHTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"accessToken":"at","refreshToken":"rt","expiresIn":3600}`),
		}, nil
	}
	callHostCall = func(method string, payload any) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}
	t.Cleanup(func() { hostHTTPDoCall, callHostCall = originalHTTP, originalCall })

	startRaw, errStart := handleConsoleOAuthStart(managementRequest{
		Method: http.MethodPost, Path: "/v0/management/plugins/kiro/oauth/login/start", Body: []byte(`{}`),
	})
	if errStart != nil {
		t.Fatalf("handleConsoleOAuthStart: %v", errStart)
	}
	_, started := decodeManagementBody(t, startRaw)
	state, _ := started["state"].(string)
	if state == "" {
		t.Fatalf("start returned no state: %v", started)
	}
	t.Cleanup(func() {
		clearBrowserLoginSession(state)
		consoleOAuthSessions.Lock()
		delete(consoleOAuthSessions.metadata, state)
		consoleOAuthSessions.Unlock()
	})

	// The panel's first poll, before the user has finished in the browser.
	pollRaw, errPoll := handleConsoleOAuthStatus(managementRequest{
		Method: http.MethodGet, Query: url.Values{"state": []string{state}},
	})
	if errPoll != nil {
		t.Fatalf("poll: %v", errPoll)
	}
	pollStatus, polled := decodeManagementBody(t, pollRaw)
	if pollStatus != http.StatusOK {
		t.Fatalf("poll: HTTP %d body=%v", pollStatus, polled)
	}
	if got, _ := polled["status"].(string); got != "pending" {
		t.Fatalf("poll before the callback must stay pending, got %v (msg=%v)", polled["status"], polled["message"])
	}

	// Now the user pastes the callback the browser produced.
	pasted := defaultRedirectURI + "/oauth/callback?code=late-paste-code&state=" + url.QueryEscape(state)
	body, _ := json.Marshal(map[string]string{"callback_url": pasted})
	raw, errPaste := handleConsoleOAuthStatus(managementRequest{
		Method: http.MethodPost, Query: url.Values{"state": []string{state}}, Body: body,
	})
	if errPaste != nil {
		t.Fatalf("paste: %v", errPaste)
	}
	status, out := decodeManagementBody(t, raw)
	if status == http.StatusNotFound {
		t.Fatalf("会话在粘贴前就被轮询删掉了: %v", out)
	}
	if status != http.StatusOK {
		t.Fatalf("paste: HTTP %d body=%v", status, out)
	}
	if got, _ := out["status"].(string); got != "success" {
		t.Fatalf("status=%v want success (msg=%v)", out["status"], out["message"])
	}
}

// A body with no callback_url must fall through to the normal poll instead of
// being rejected — the panel polls this same endpoint without a body, and the
// poll loop keeps running after a paste.
func TestStatusPostWithoutCallbackURLStillPolls(t *testing.T) {
	const state = "11112222-3333-4444-5555-666677778888"
	seedConsoleOAuthSession(t, state)

	originalHTTP, originalCall := hostHTTPDoCall, callHostCall
	hostHTTPDoCall = func(req hostHTTPRequest) (hostHTTPResponse, error) {
		return hostHTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"accessToken":"at","refreshToken":"rt","expiresIn":3600}`),
		}, nil
	}
	callHostCall = func(method string, payload any) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}
	t.Cleanup(func() { hostHTTPDoCall, callHostCall = originalHTTP, originalCall })

	// A bodyless POST must reach the poll, not be turned away as
	// invalid_callback. (The poll itself reports error here because the console
	// path carries no auth dir for the on-disk fallback — the in-memory
	// session callback is what matters.)
	raw, errHandle := handleConsoleOAuthStatus(managementRequest{
		Method: http.MethodPost, Query: url.Values{"state": []string{state}}, Body: []byte(`{}`),
	})
	if errHandle != nil {
		t.Fatalf("handleConsoleOAuthStatus: %v", errHandle)
	}
	status, out := decodeManagementBody(t, raw)
	if status == http.StatusBadRequest {
		t.Fatalf("bodyless POST must fall through to the poll, got %v", out)
	}
	if got, _ := out["error"].(string); got == "invalid_callback" {
		t.Fatalf("bodyless POST must not be treated as a bad callback: %v", out)
	}
}
