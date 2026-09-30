package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/varcli/cpa-plugins/plugins/cline/clinenx"
)

// loginStates holds in-flight WorkOS device grants keyed by the opaque state the
// panel polls with. A background pruner drops expired entries so a panel that is
// closed mid-login does not leak them for the process lifetime.
var (
	loginStates sync.Map
	loginOnce   sync.Once
)

func startLoginPruner() {
	loginOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				now := time.Now()
				loginStates.Range(func(key, value any) bool {
					if state, ok := value.(*loginState); ok && now.After(state.Expires) {
						loginStates.Delete(key)
					}
					return true
				})
			}
		}()
	})
}

// parseAuth answers auth.parse: recognize a stored Cline credential and hand it
// back as host auth data.
func parseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	sa, err := parseStored(req.RawJSON)
	if err != nil || strings.TrimSpace(sa.Auth.AccessToken) == "" {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	return okEnvelope(pluginapi.AuthParseResponse{
		Handled: true,
		Auth:    authDataFromStored(req.FileName, sa),
	})
}

func startLogin(raw []byte) ([]byte, error) {
	startLoginPruner()
	resp, err := beginLogin()
	if err != nil {
		return nil, err
	}
	return okEnvelope(resp)
}

// beginLogin opens a WorkOS device flow and registers the pending state so the
// panel can poll it by state token.
func beginLogin() (pluginapi.AuthLoginStartResponse, error) {
	form := "client_id=" + workOSClientID
	response, err := clineRequest(http.MethodPost, workOSAPIBase+"/user_management/authorize/device",
		"application/x-www-form-urlencoded", strings.NewReader(form), nil)
	if err != nil {
		return pluginapi.AuthLoginStartResponse{}, err
	}
	var device workOSDeviceResponse
	if err := json.Unmarshal(response.Body, &device); err != nil {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("decode WorkOS device response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || device.DeviceCode == "" {
		msg := clinenx.NonEmpty(device.ErrorDescription, clinenx.NonEmpty(device.Error, string(response.Body)))
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("WorkOS device authorization failed: %s", clinenx.Truncate(msg, 240))
	}

	resultURL := buildDeviceLoginURL(device)
	verification := clinenx.NonEmpty(device.VerificationURI, device.VerificationURIComplete)
	state := randomID()
	expiresIn := time.Duration(device.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = 5 * time.Minute
	}
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	loginStates.Store(state, &loginState{
		DeviceCode:   device.DeviceCode,
		UserCode:     device.UserCode,
		Verification: verification,
		Interval:     interval,
		Expires:      time.Now().Add(expiresIn),
		Provider:     providerName,
		StartedAt:    time.Now(),
	})
	return pluginapi.AuthLoginStartResponse{
		Provider:  providerName,
		URL:       resultURL,
		State:     state,
		ExpiresAt: time.Now().Add(expiresIn),
	}, nil
}

// buildDeviceLoginURL returns the browser URL for a WorkOS device grant.
//
// WorkOS already returns verification_uri_complete with the user_code query
// parameter embedded. Appending it again produced
// "...?user_code=ABC?user_code=ABC", which browsers treat as a broken URL.
func buildDeviceLoginURL(device workOSDeviceResponse) string {
	if complete := strings.TrimSpace(device.VerificationURIComplete); complete != "" {
		return complete
	}
	base := strings.TrimSpace(device.VerificationURI)
	if base == "" || strings.TrimSpace(device.UserCode) == "" {
		return base
	}
	if strings.Contains(base, "user_code=") {
		return base
	}
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	return base + separator + "user_code=" + url.QueryEscape(strings.TrimSpace(device.UserCode))
}

func pollLogin(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginPollRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	return okEnvelope(pollLoginState(req.State))
}

func pollLoginState(stateKey string) pluginapi.AuthLoginPollResponse {
	value, ok := loginStates.Load(stateKey)
	if !ok {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "login state not found or expired; restart login",
		}
	}
	state := value.(*loginState)
	if time.Now().After(state.Expires) {
		loginStates.Delete(stateKey)
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "login expired; restart login",
		}
	}
	token, pending, err := pollWorkOSToken(state)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: err.Error()}
	}
	if pending {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "waiting for browser authentication confirmation",
		}
	}
	auth, err := registerWorkOSTokens(token)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: err.Error()}
	}
	loginStates.Delete(stateKey)
	return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusSuccess, Auth: auth}
}

func pollWorkOSToken(state *loginState) (workOSTokenResponse, bool, error) {
	form := "grant_type=urn:ietf:params:oauth:grant-type:device_code" +
		"&device_code=" + url.QueryEscape(state.DeviceCode) +
		"&client_id=" + workOSClientID
	response, err := clineRequest(http.MethodPost, workOSAPIBase+"/user_management/authenticate",
		"application/x-www-form-urlencoded", strings.NewReader(form), nil)
	if err != nil {
		return workOSTokenResponse{}, false, err
	}
	var token workOSTokenResponse
	if err := json.Unmarshal(response.Body, &token); err != nil {
		return workOSTokenResponse{}, false, fmt.Errorf("decode WorkOS token response: %w", err)
	}
	// Both of these mean "keep polling"; they are the normal state while the
	// user is still in the browser, not an error.
	if token.Error == "authorization_pending" || token.Error == "slow_down" {
		return token, true, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		msg := clinenx.NonEmpty(token.ErrorDescription, clinenx.NonEmpty(token.Error, string(response.Body)))
		return token, false, fmt.Errorf("WorkOS token exchange failed: %s", clinenx.Truncate(msg, 240))
	}
	if token.AccessToken == "" || token.RefreshToken == "" {
		return token, false, fmt.Errorf("WorkOS token response is incomplete")
	}
	return token, false, nil
}

// registerWorkOSTokens exchanges a WorkOS grant for Cline's own token pair.
func registerWorkOSTokens(token workOSTokenResponse) (pluginapi.AuthData, error) {
	payload := map[string]string{
		"accessToken":  token.AccessToken,
		"refreshToken": token.RefreshToken,
	}
	response, err := postJSON(clineAPIBase+"/api/v1/auth/register", payload, nil)
	if err != nil {
		return pluginapi.AuthData{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return pluginapi.AuthData{}, fmt.Errorf("Cline token registration failed: HTTP %d: %s",
			response.StatusCode, clinenx.Truncate(string(response.Body), 240))
	}
	var parsed clineAuthResponse
	if err := json.Unmarshal(response.Body, &parsed); err != nil {
		return pluginapi.AuthData{}, fmt.Errorf("decode Cline token response: %w", err)
	}
	if !parsed.Success || parsed.Data.AccessToken == "" {
		return pluginapi.AuthData{}, fmt.Errorf("Cline token registration returned no access token")
	}
	sa := &storedAuth{
		Auth: storedTokens{
			AccessToken:  parsed.Data.AccessToken,
			RefreshToken: parsed.Data.RefreshToken,
			ExpiresAt:    parseExpiryMillis(parsed.Data.ExpiresAt),
			TokenType:    parsed.Data.TokenType,
		},
		Account: storedAccount{
			ID:          parsed.Data.UserInfo.ClineUID,
			Email:       parsed.Data.UserInfo.Email,
			DisplayName: parsed.Data.UserInfo.Name,
		},
	}
	return authDataFromStored(authFileName, sa), nil
}

// refreshAuth answers auth.refresh.
func refreshAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthRefreshRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	// The executor rotates credentials on its own, so the host can ask for a
	// refresh against a snapshot the plugin has already superseded. Reusing the
	// cached pair there keeps a rotated refresh token from being spent twice.
	cred := newCredential(sa, req.Attributes)
	if !cred.adopted {
		if err := cred.prepare(true); err != nil {
			return nil, credentialFailure(err)
		}
		cred.enrichAndPersist(fetchAccountSnapshot)
	}
	return okEnvelope(pluginapi.AuthRefreshResponse{
		Auth:             authDataFromStored(req.AuthID, cred.sa),
		NextRefreshAfter: nextCredentialRefreshAt(cred.sa),
	})
}

// parseStored decodes a Cline credential in either the nested plugin format
// ({auth, account}) or the flat legacy shape.
func parseStored(raw []byte) (*storedAuth, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty auth storage")
	}
	var nested struct {
		Auth    storedTokens  `json:"auth"`
		Account storedAccount `json:"account"`
		Label   string        `json:"label"`
		Note    string        `json:"note"`
	}
	if err := json.Unmarshal(raw, &nested); err == nil && nested.Auth.AccessToken != "" {
		sa := &storedAuth{Auth: nested.Auth, Account: nested.Account}
		// Older plugin builds stored the custom display name only in the
		// host-level note (or in label after changing it externally). Import
		// that value so deployments do not silently lose a rename.
		if strings.TrimSpace(sa.Account.Nickname) == "" {
			sa.Account.Nickname = legacyNickname(nested.Note, nested.Label, &sa.Account)
		}
		return sa, nil
	}
	var flat struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
		Email        string `json:"email"`
		DisplayName  string `json:"displayName"`
		Nickname     string `json:"nickname"`
		ID           string `json:"id"`
	}
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, fmt.Errorf("storage_parse_error: %w", err)
	}
	if flat.AccessToken == "" {
		return nil, fmt.Errorf("storage_parse_error: access token is missing")
	}
	return &storedAuth{
		Auth: storedTokens{
			AccessToken:  flat.AccessToken,
			RefreshToken: flat.RefreshToken,
			ExpiresAt:    flat.ExpiresAt,
		},
		Account: storedAccount{
			ID:          flat.ID,
			Email:       flat.Email,
			DisplayName: flat.DisplayName,
			Nickname:    flat.Nickname,
		},
	}, nil
}

// legacyNickname recovers a custom display name from the host-level note or
// label, ignoring a label that is just the derived account identity.
func legacyNickname(note, label string, account *storedAccount) string {
	if value := strings.TrimSpace(note); value != "" {
		return value
	}
	label = strings.TrimSpace(label)
	if label == "" || account == nil {
		return ""
	}
	if label == strings.TrimSpace(account.DisplayName) || label == strings.TrimSpace(account.Email) {
		return ""
	}
	return label
}

// authDataFromStored renders host auth data for one credential.
func authDataFromStored(id string, sa *storedAuth) pluginapi.AuthData {
	if sa == nil {
		return pluginapi.AuthData{Provider: providerName}
	}
	raw, _ := json.Marshal(sa)
	if strings.TrimSpace(id) == "" {
		id = authFileName
	}
	metadata := map[string]any{}
	if sa.Account.Email != "" {
		metadata["email"] = sa.Account.Email
	}
	if sa.Account.Nickname != "" {
		metadata["nickname"] = sa.Account.Nickname
	}
	if sa.Account.DisplayName != "" {
		metadata["display_name"] = sa.Account.DisplayName
	}
	if sa.Account.Plan != "" {
		metadata["plan"] = sa.Account.Plan
	}
	if sa.Account.PlanStatus != "" {
		metadata["plan_status"] = sa.Account.PlanStatus
	}
	return pluginapi.AuthData{
		Provider:    providerName,
		ID:          id,
		FileName:    authFileName,
		Label:       clineAccountLabel(sa),
		StorageJSON: raw,
		Metadata:    metadata,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			// Cline's access token lives one hour, and CPA only schedules a
			// refresh for providers that declare a cadence. Without this the host
			// never calls AuthRefresh and the account goes dark on the hour; the
			// plugin's own pre-request refresh is the safety net.
			authRefreshIntervalAttribute: strconv.Itoa(int(credentialRefreshLead / time.Second)),
		},
		NextRefreshAfter: nextCredentialRefreshAt(sa),
	}
}

// parseExpiryMillis reads Cline's RFC3339 expiry, falling back to a conservative
// hour minus a small margin so a missing field cannot look freshly valid.
func parseExpiryMillis(value string) int64 {
	if value == "" {
		return time.Now().Add(55 * time.Minute).UnixMilli()
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Now().Add(55 * time.Minute).UnixMilli()
	}
	return parsed.UnixMilli()
}
