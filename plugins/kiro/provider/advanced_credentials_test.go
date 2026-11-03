package provider

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAPIKeyCredentialUsesAPIKeyBearerAndSkipsRefresh(t *testing.T) {
	cred := credential{AuthMethod: authMethodAPIKey, KiroAPIKey: "ksk_test", SSORegion: defaultRegion, APIRegion: defaultRegion}
	if cred.bearerToken() != "ksk_test" || cred.tokenType() != "API_KEY" {
		t.Fatalf("api key bearer/type = %q/%q", cred.bearerToken(), cred.tokenType())
	}
	if credentialNeedsRefresh(cred) {
		t.Fatal("API key was scheduled for token refresh")
	}
	refreshed, err := refreshCredential(cred, "")
	if err != nil || refreshed.KiroAPIKey != "ksk_test" {
		t.Fatalf("API key refresh = %+v, %v", refreshed, err)
	}
	headers := kiroHeaders(cred)
	if headers.Get("Authorization") != "Bearer ksk_test" || headers.Get("TokenType") != "API_KEY" {
		t.Fatalf("API key headers = %v", headers)
	}
}

func TestExternalIDPRefreshUsesFormAndTokenType(t *testing.T) {
	originalHTTP := hostHTTPDoCall
	t.Cleanup(func() { hostHTTPDoCall = originalHTTP })
	hostHTTPDoCall = func(req hostHTTPRequest) (hostHTTPResponse, error) {
		if req.URL != "https://idp.example.test/token" {
			t.Fatalf("refresh URL = %q", req.URL)
		}
		if !strings.Contains(string(req.Body), "grant_type=refresh_token") || !strings.Contains(string(req.Body), "client_id=client-test") {
			t.Fatalf("refresh body = %q", req.Body)
		}
		return hostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)}, nil
	}
	cred := credential{
		AuthMethod: authMethodExternalIDP, ClientID: "client-test", RefreshToken: "old-refresh",
		TokenEndpoint: "https://idp.example.test/token", SSORegion: defaultRegion, APIRegion: defaultRegion,
	}
	refreshed, err := refreshCredential(cred, "")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken != "new-access" || refreshed.RefreshToken != "new-refresh" || refreshed.bearerToken() != "new-access" {
		t.Fatalf("refreshed credential = %+v", refreshed)
	}
	if refreshed.tokenType() != "EXTERNAL_IDP" {
		t.Fatalf("token type = %q", refreshed.tokenType())
	}
}

func TestImportedCredentialSupportsAPIKeyAndExternalIDP(t *testing.T) {
	apiKey, err := importedCredential(credentialImportRequest{AuthMethod: "api_key", KiroAPIKey: "ksk_import"})
	if err != nil || apiKey.AuthMethod != authMethodAPIKey || apiKey.AuthType != "kiro_api_key" {
		t.Fatalf("api key import = %+v, %v", apiKey, err)
	}
	external, err := importedCredential(credentialImportRequest{
		AuthMethod: "external_idp", RefreshToken: "rt", ClientID: "client",
		TokenEndpoint: "https://idp.example.test/token", Region: "us-east-1",
	})
	if err != nil || external.AuthMethod != authMethodExternalIDP || external.AuthType != "external_idp" {
		t.Fatalf("external import = %+v, %v", external, err)
	}
}

func TestBlockedCredentialIsDisabledAndNoted(t *testing.T) {
	raw := blockedStorage([]byte(`{"type":"kiro","note":"mine","refreshToken":"old"}`), credential{RefreshToken: "new"}, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil {
		t.Fatal("blocked storage is invalid JSON")
	}
	if fields["disabled"] != true || fields["note"] != "mine" || fields[blockedAtKey] == nil || fields["refresh_token"] != "new" {
		t.Fatalf("blocked fields = %v", fields)
	}
}

func TestCredentialMethodAndIdentityCompatibility(t *testing.T) {
	for raw, wantMethod := range map[string]string{
		`{"authMethod":"api_key","kiroApiKey":"ksk_compat"}`:                                            authMethodAPIKey,
		`{"authMethod":"external_idp","tokenEndpoint":"https://idp.example.test/token","clientId":"c"}`: authMethodExternalIDP,
		`{"authMethod":"idc","clientId":"c","clientSecret":"s"}`:                                        authMethodIDC,
		`{"refreshToken":"rt"}`: authMethodSocial,
	} {
		cred, err := decodeCredential([]byte(raw))
		if err != nil || cred.AuthMethod != wantMethod {
			t.Fatalf("%s: method=%q err=%v", raw, cred.AuthMethod, err)
		}
	}

	first := credential{AuthMethod: authMethodSocial, UserID: "user-1", Email: "same@example.com", RefreshToken: "one"}
	second := credential{AuthMethod: authMethodSocial, UserID: "user-1", Email: "same@example.com", RefreshToken: "two"}
	if credentialID(first) != credentialID(second) || first.fileName() != second.fileName() {
		t.Fatalf("account identity did not deduplicate: %q/%q vs %q/%q", credentialID(first), first.fileName(), credentialID(second), second.fileName())
	}
}

func TestGlobalModelExclusionsApplyToPrefixedAndNativeIDs(t *testing.T) {
	models := []modelInfo{{ID: "kiro/claude-opus-4.7"}, {ID: "kiro/claude-sonnet-4.6"}}
	host := hostConfigSummary{ExcludedModels: map[string][]string{providerID: {"claude-opus-*"}}}
	filtered := withoutGlobalExclusions(models, host)
	if len(filtered) != 1 || filtered[0].ID != "kiro/claude-sonnet-4.6" {
		t.Fatalf("filtered models = %+v", filtered)
	}
}
