package provider

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// useSocialDeviceConfig pins the plugin config to the social device flow for
// one test and restores it afterwards.
func useSocialDeviceConfig(t *testing.T, socialProvider string) {
	t.Helper()
	original := loadedConfig()
	config := original
	config.LoginMode = socialDeviceLoginMode
	config.SocialProvider = socialProvider
	config.DesktopTokenURL = defaultTokenURL
	configValue.Store(config)
	t.Cleanup(func() { configValue.Store(original) })
}

// The social device flow exists because Kiro's desktop token endpoint rejects
// the authorization codes that the browser flow receives for Google/GitHub
// accounts ("Oops, something went wrong. Please try again later."). This drives
// the whole flow end to end — authorization, poll, credential construction —
// against a stubbed Kiro so the redirect/token-exchange path is never touched.
func TestSocialDeviceLoginReturnsCredentials(t *testing.T) {
	useSocialDeviceConfig(t, "github")

	originalHTTP := hostHTTPDoCall
	t.Cleanup(func() { hostHTTPDoCall = originalHTTP })

	requests := 0
	hostHTTPDoCall = func(req hostHTTPRequest) (hostHTTPResponse, error) {
		requests++
		var payload map[string]string
		if errDecode := json.Unmarshal(req.Body, &payload); errDecode != nil {
			t.Fatal(errDecode)
		}
		switch req.URL {
		case "https://prod.us-east-1.auth.desktop.kiro.dev/oauth/device/authorization":
			if payload["clientId"] != socialDeviceClientID || payload["loginProvider"] != "Github" {
				t.Fatalf("authorization payload = %#v", payload)
			}
			return hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{
				"deviceCode":"device-123","userCode":"ABCD-EFGH",
				"verificationUri":"https://app.kiro.dev/account/device",
				"verificationUriComplete":"https://app.kiro.dev/account/device?user_code=ABCD-EFGH&login_provider=Github",
				"expiresInMilliseconds":300000,"intervalInMilliseconds":5000
			}`)}, nil
		case "https://prod.us-east-1.auth.desktop.kiro.dev/oauth/device/poll":
			if payload["clientId"] != socialDeviceClientID || payload["deviceCode"] != "device-123" {
				t.Fatalf("poll payload = %#v", payload)
			}
			return hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{
				"status":"success","accessToken":"access-123","refreshToken":"refresh-123",
				"profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/social","expiresIn":3600
			}`)}, nil
		default:
			t.Fatalf("unexpected URL %q", req.URL)
			return hostHTTPResponse{}, nil
		}
	}

	startRaw, errStart := startLogin([]byte(`{"Provider":"kiro","host_callback_id":"start-callback"}`))
	if errStart != nil {
		t.Fatal(errStart)
	}
	var startEnvelope envelope
	if errDecode := json.Unmarshal(startRaw, &startEnvelope); errDecode != nil || !startEnvelope.OK {
		t.Fatalf("start envelope = %s, err = %v", startRaw, errDecode)
	}
	var start authLoginStartResponse
	if errDecode := json.Unmarshal(startEnvelope.Result, &start); errDecode != nil {
		t.Fatal(errDecode)
	}
	if start.URL != "https://app.kiro.dev/account/device?user_code=ABCD-EFGH&login_provider=Github" {
		t.Fatalf("start URL = %q", start.URL)
	}
	t.Cleanup(func() { clearDeviceLoginPoll(start.State) })
	setNextDeviceLoginPoll(start.State, time.Now().UTC().Add(-time.Second))

	pollRequest, _ := json.Marshal(authLoginPollRequest{
		Provider: providerID, State: start.State, Metadata: start.Metadata, HostCallbackID: "poll-callback",
	})
	pollRaw, errPoll := pollLogin(pollRequest)
	if errPoll != nil {
		t.Fatal(errPoll)
	}
	var pollEnvelope envelope
	_ = json.Unmarshal(pollRaw, &pollEnvelope)
	var poll authLoginPollResponse
	_ = json.Unmarshal(pollEnvelope.Result, &poll)
	if poll.Status != "success" {
		t.Fatalf("poll response = %s", pollRaw)
	}
	var stored credential
	if errDecode := json.Unmarshal(poll.Auth.StorageJSON, &stored); errDecode != nil {
		t.Fatal(errDecode)
	}
	if stored.AccessToken != "access-123" || stored.RefreshToken != "refresh-123" || stored.SourceKind != "oauth_social_device" || stored.SocialProvider != "github" || stored.Label != "Kiro GitHub" {
		t.Fatalf("stored credential = %#v", stored)
	}
	wantAuthID := oauthCredentialID("github", start.State)
	if poll.Auth.ID != wantAuthID || poll.Auth.FileName != wantAuthID+".json" || stored.AuthID != wantAuthID {
		t.Fatalf("social auth identity = ID %q, file %q, stored ID %q; want %q", poll.Auth.ID, poll.Auth.FileName, stored.AuthID, wantAuthID)
	}
	if poll.Auth.Metadata["social_provider"] != "github" || poll.Auth.Attributes["social_provider"] != "github" {
		t.Fatalf("social auth provider metadata = %#v / %#v", poll.Auth.Metadata, poll.Auth.Attributes)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want 2", requests)
	}
}

// The panel's provider selector arrives as social_provider metadata with no
// login_mode alongside it. That override has to reach the start step even when
// the configured mode is what chooses the flow — otherwise picking GitHub in
// the panel would silently authorize against the configured Google instead.
func TestSocialDeviceLoginHonoursProviderOverride(t *testing.T) {
	useSocialDeviceConfig(t, "google")

	originalHTTP := hostHTTPDoCall
	t.Cleanup(func() { hostHTTPDoCall = originalHTTP })
	hostHTTPDoCall = func(req hostHTTPRequest) (hostHTTPResponse, error) {
		var payload map[string]string
		if errDecode := json.Unmarshal(req.Body, &payload); errDecode != nil {
			t.Fatal(errDecode)
		}
		if payload["loginProvider"] != "Github" {
			t.Fatalf("loginProvider = %q, want Github (panel override ignored)", payload["loginProvider"])
		}
		return hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{
			"deviceCode":"device-override","userCode":"OVRD-0001",
			"verificationUriComplete":"https://app.kiro.dev/account/device?user_code=OVRD-0001",
			"expiresInMilliseconds":300000,"intervalInMilliseconds":5000
		}`)}, nil
	}

	raw, errStart := startLogin([]byte(`{"Provider":"kiro","Metadata":{"social_provider":"github"}}`))
	if errStart != nil {
		t.Fatal(errStart)
	}
	var env envelope
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil || !env.OK {
		t.Fatalf("start envelope = %s, err = %v", raw, errDecode)
	}
	var started authLoginStartResponse
	if errDecode := json.Unmarshal(env.Result, &started); errDecode != nil {
		t.Fatal(errDecode)
	}
	t.Cleanup(func() { clearDeviceLoginPoll(started.State) })
	if got := started.Metadata["social_provider"]; got != "github" {
		t.Fatalf("session social_provider = %v, want github", got)
	}
	loginState, errState := decodeSocialDeviceLoginState(started.Metadata)
	if errState != nil {
		t.Fatal(errState)
	}
	if loginState.SocialProvider != "github" {
		t.Fatalf("decoded social provider = %q", loginState.SocialProvider)
	}
}

// Signing the same provider in twice must add a second account rather than
// overwrite the first, and the two providers must not collide either.
func TestSocialOAuthAuthorizationsUseDistinctCredentialIDs(t *testing.T) {
	profileARN := "arn:aws:codewhisperer:us-east-1:123456789012:profile/social"
	first := credential{
		Version: 1, AuthID: oauthCredentialID("github", "authorization-one"),
		SourceKind: "oauth_social_device", ProfileARN: profileARN, RefreshToken: "shared-refresh-token",
	}
	second := first
	second.AuthID = oauthCredentialID("github", "authorization-two")
	google := first
	google.AuthID = oauthCredentialID("google", "authorization-one")

	firstAuth, errFirst := authDataFromCredential(first)
	secondAuth, errSecond := authDataFromCredential(second)
	googleAuth, errGoogle := authDataFromCredential(google)
	if errFirst != nil || errSecond != nil || errGoogle != nil {
		t.Fatalf("build auth data: first=%v second=%v google=%v", errFirst, errSecond, errGoogle)
	}
	if firstAuth.ID == secondAuth.ID || firstAuth.FileName == secondAuth.FileName {
		t.Fatalf("repeated GitHub authorizations collided: %#v and %#v", firstAuth, secondAuth)
	}
	if firstAuth.ID == googleAuth.ID || firstAuth.FileName == googleAuth.FileName {
		t.Fatalf("GitHub and Google authorizations collided: %#v and %#v", firstAuth, googleAuth)
	}
}

// An unfinished authorization is the normal case for most of the login: the
// poll must report pending, not an error, or the panel would abandon the login
// the moment the user is still reading the device page.
func TestSocialDevicePollTreatsAuthorizationPendingAsPending(t *testing.T) {
	originalHTTP := hostHTTPDoCall
	t.Cleanup(func() { hostHTTPDoCall = originalHTTP })
	state := socialDeviceLoginState{
		Version: 1, LoginMode: socialDeviceLoginMode, State: "pending-state", ClientID: socialDeviceClientID,
		DeviceCode: "pending-device", SocialProvider: "google", PollURL: "https://auth.fixture.invalid/oauth/device/poll",
		APIRegion: defaultRegion, ExpiresAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339), IntervalMilliseconds: 5000,
	}
	hostHTTPDoCall = func(req hostHTTPRequest) (hostHTTPResponse, error) {
		var payload map[string]string
		_ = json.Unmarshal(req.Body, &payload)
		if payload["clientId"] != socialDeviceClientID || payload["deviceCode"] != "pending-device" {
			t.Fatalf("poll payload = %#v", payload)
		}
		return hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{
			"accessToken":null,"refreshToken":null,"profileArn":null,"status":"authorization_pending"
		}`)}, nil
	}
	metadataRaw, _ := json.Marshal(state)
	var metadata map[string]any
	_ = json.Unmarshal(metadataRaw, &metadata)
	setNextDeviceLoginPoll(state.State, time.Now().UTC().Add(-time.Second))
	t.Cleanup(func() { clearDeviceLoginPoll(state.State) })

	raw, errPoll := pollSocialDeviceLoginRequest(authLoginPollRequest{State: state.State, Metadata: metadata})
	if errPoll != nil {
		t.Fatal(errPoll)
	}
	var result envelope
	_ = json.Unmarshal(raw, &result)
	var response authLoginPollResponse
	_ = json.Unmarshal(result.Result, &response)
	if response.Status != "pending" {
		t.Fatalf("poll response = %s", raw)
	}
}

// The verification URL is opened by the user's browser, so a spoofed or
// misconfigured upstream must not be able to redirect it off Kiro's own host.
func TestSocialDeviceRejectsUntrustedVerificationURL(t *testing.T) {
	if isTrustedSocialVerificationURL("https://app.kiro.dev/account/device?user_code=X") != true {
		t.Fatal("app.kiro.dev should be trusted")
	}
	for _, raw := range []string{
		"http://app.kiro.dev/account/device",
		"https://app.kiro.dev.evil.example/account/device",
		"https://evil.example/account/device",
		"https://user:pass@app.kiro.dev/account/device",
		"javascript:alert(1)",
		"",
	} {
		if isTrustedSocialVerificationURL(raw) {
			t.Fatalf("verification URL %q must not be trusted", raw)
		}
	}
}

// The endpoints are derived from desktop_token_url so an override (private
// gateway, test fixture) moves the device endpoints with it.
func TestSocialDeviceEndpointsDeriveFromTokenURL(t *testing.T) {
	authorizationURL, pollURL, errEndpoints := socialDeviceEndpoints(defaultTokenURL)
	if errEndpoints != nil {
		t.Fatal(errEndpoints)
	}
	if authorizationURL != "https://prod.us-east-1.auth.desktop.kiro.dev/oauth/device/authorization" {
		t.Fatalf("authorization URL = %q", authorizationURL)
	}
	if pollURL != "https://prod.us-east-1.auth.desktop.kiro.dev/oauth/device/poll" {
		t.Fatalf("poll URL = %q", pollURL)
	}
	for _, raw := range []string{"http://prod.us-east-1.auth.desktop.kiro.dev/oauth/token", "not a url", ""} {
		if _, _, errInvalid := socialDeviceEndpoints(raw); errInvalid == nil {
			t.Fatalf("token URL %q must be rejected", raw)
		}
	}
}
