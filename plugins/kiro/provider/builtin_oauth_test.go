package provider

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The built-in CPA flow (v8/management/oauth/auth-url -> StartOAuthV8) calls
// auth.login.start with the host's callback base URL and no metadata. Whatever
// URL the plugin returns is what CPA shows the user, so returning anything
// other than the real upstream authorization URL breaks that flow.
func TestBuiltinStartReturnsRealAuthorizationURL(t *testing.T) {
	originalHTTP := hostHTTPDoCall
	t.Cleanup(func() { hostHTTPDoCall = originalHTTP })
	hostHTTPDoCall = func(req hostHTTPRequest) (hostHTTPResponse, error) {
		return hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{
			"deviceCode":"d1","userCode":"ABCD",
			"verificationUriComplete":"https://app.kiro.dev/account/device?user_code=ABCD",
			"expiresInMilliseconds":300000,"intervalInMilliseconds":5000
		}`)}, nil
	}

	req := authLoginStartRequest{
		Provider: providerID,
		BaseURL:  "http://127.0.0.1:8317/v0/management/oauth-callback",
		Host:     hostConfigSummary{AuthDir: t.TempDir()},
	}
	raw, errStart := startLogin(mustJSON(req))
	if errStart != nil {
		t.Fatal(errStart)
	}
	var env envelope
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil || !env.OK {
		t.Fatalf("envelope = %s", raw)
	}
	var started authLoginStartResponse
	if errDecode := json.Unmarshal(env.Result, &started); errDecode != nil {
		t.Fatal(errDecode)
	}
	t.Cleanup(func() { clearDeviceLoginPoll(started.State) })

	if !strings.HasPrefix(started.URL, "https://app.kiro.dev/") {
		t.Fatalf("built-in start returned URL %q; CPA shows this to the user, so the device flow can never be completed from the built-in UI", started.URL)
	}
}

// The panel path sends metadata, which historically suppressed the console
// rewrite. Both paths must still hand back a usable URL.
func TestStartURLIsUsableOnBothPaths(t *testing.T) {
	originalHTTP := hostHTTPDoCall
	t.Cleanup(func() { hostHTTPDoCall = originalHTTP })
	hostHTTPDoCall = func(req hostHTTPRequest) (hostHTTPResponse, error) {
		return hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{
			"deviceCode":"d2","userCode":"EFGH",
			"verificationUriComplete":"https://app.kiro.dev/account/device?user_code=EFGH",
			"expiresInMilliseconds":300000,"intervalInMilliseconds":5000
		}`)}, nil
	}
	for name, req := range map[string]authLoginStartRequest{
		"builtin": {Provider: providerID, BaseURL: "http://127.0.0.1:8317/v0/management/oauth-callback"},
		"panel":   {Provider: providerID, BaseURL: "http://127.0.0.1:8317/v0/management/oauth-callback", Metadata: map[string]any{"social_provider": "github"}},
	} {
		raw, errStart := startLogin(mustJSON(req))
		if errStart != nil {
			t.Fatalf("%s: %v", name, errStart)
		}
		var env envelope
		_ = json.Unmarshal(raw, &env)
		var started authLoginStartResponse
		_ = json.Unmarshal(env.Result, &started)
		t.Cleanup(func() { clearDeviceLoginPoll(started.State) })
		if !strings.HasPrefix(started.URL, "https://app.kiro.dev/") {
			t.Fatalf("%s path returned URL %q", name, started.URL)
		}
		if started.State == "" {
			t.Fatalf("%s path returned no state", name)
		}
	}
}

// The host validates the state it hands back to the browser and later uses it
// as the on-disk callback filename, so it must survive ValidateOAuthState's
// character set.
func TestLoginStateIsHostCompatible(t *testing.T) {
	state := randomID()
	if strings.TrimSpace(state) == "" {
		t.Fatal("empty state")
	}
	if strings.ContainsAny(state, "/\\") || strings.Contains(state, "..") {
		t.Fatalf("state %q contains a path separator", state)
	}
	for _, r := range state {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			t.Fatalf("state %q contains character %q that the host rejects", state, r)
		}
	}
	if len(state) > 128 {
		t.Fatalf("state %q exceeds the host's 128-character limit", state)
	}
	_ = time.Now()
}
