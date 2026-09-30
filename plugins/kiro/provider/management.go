package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// management.go registers the Kiro management API and web panel.
//
// Routes are exposed under two prefixes:
//   - /v0/management/plugins/kiro/*  — JSON endpoints (CPA-management-
//     authenticated by host middleware).
//   - /v0/resource/plugins/kiro/*    — unauthenticated browser UI (panel.html)
//     and menu entries surfaced in the CPA management UI.
//
// The contract matches trae/qoder/workbuddy: handleManagement returns an
// envelope-wrapped pluginapi.ManagementResponse. CPA's rpcPluginAdapter
// decodes the envelope, and its HTTP layer writes only resp.Body to the
// browser, so JSON.parse / HTML rendering work transparently.

type managementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

// registerManagement describes the routes + resources this plugin serves.
// Paths are relative to /plugins/kiro — the host prepends /v0/management for
// Routes and /v0/resource/plugins/kiro for Resources.
func registerManagement() ([]byte, error) {
	base := "/plugins/" + providerName
	return okEnvelope(managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: base + "/quota", Description: "Returns sanitized Kiro quota and usage-limit data."},
			{Method: http.MethodPost, Path: base + "/quotaRequest", Description: "Refreshes Kiro quota without sending model requests."},
			{Method: http.MethodGet, Path: base + "/credentials", Description: "Returns sanitized CPA credential records and request statistics."},
			{Method: http.MethodPost, Path: base + "/oauth/relogin/start", Description: "Starts Kiro OAuth again and replaces an existing Kiro credential."},
			{Method: http.MethodGet, Path: base + "/oauth/relogin/status", Description: "Polls a Kiro credential replacement login."},
			{Method: http.MethodPost, Path: base + "/oauth/login/start", Description: "Starts a Kiro OAuth login from the panel."},
			{Method: http.MethodGet, Path: base + "/oauth/login/status", Description: "Polls a panel-started Kiro OAuth login."},
			{Method: http.MethodPost, Path: base + "/oauth/login/status", Description: "Submits a browser OAuth callback from the panel."},
			{Method: http.MethodPost, Path: base + "/oauth/callback", Description: "Accepts a Kiro browser callback and continues organization sign-in through AWS SSO OIDC."},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "Kiro", Description: "Kiro dashboard: accounts, quota, request activity, and OAuth login."},
			{Path: "/oauth_callback", Description: "Kiro OAuth login callback redirect target."},
		},
	})
}

// handleManagement dispatches one ManagementRequest to the right handler and
// returns an envelope-wrapped pluginapi.ManagementResponse.
func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Kiro management request: %w", errUnmarshal)
	}
	path := strings.TrimRight(strings.TrimSpace(req.Path), "/")

	// Browser UI resource routes (unauthenticated).
	resPrefix := "/v0/resource/plugins/" + providerName
	if req.Method == http.MethodGet && path == resPrefix+"/oauth_callback" {
		return handleBrowserCallbackResource(req)
	}
	if req.Method == http.MethodGet && strings.HasPrefix(path, resPrefix) {
		return okEnvelope(managementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}, "Cache-Control": []string{"no-store"}},
			Body:       panelHTML,
		})
	}

	// JSON API routes (host-authenticated).
	base := loadedManagementBasePath() + "/plugins/" + providerName
	switch {
	case req.Method == http.MethodPost && path == base+"/oauth/relogin/start":
		return handleReloginStart(req)
	case (req.Method == http.MethodGet || req.Method == http.MethodPost) && path == base+"/oauth/relogin/status":
		return handleReloginStatus(req)
	case req.Method == http.MethodPost && path == base+"/oauth/login/start":
		return handleConsoleOAuthStart(req)
	case (req.Method == http.MethodGet || req.Method == http.MethodPost) && path == base+"/oauth/login/status":
		return handleConsoleOAuthStatus(req)
	case req.Method == http.MethodPost && path == base+"/oauth/callback":
		return handleBrowserCallbackManagement(req)
	case req.Method == http.MethodGet && path == base+"/credentials":
		return handleCredentialRecords()
	case path == base+"/quota" || path == base+"/quotaRequest":
		refreshRequest := path == base+"/quotaRequest"
		if !strings.EqualFold(req.Method, http.MethodGet) && !(refreshRequest && strings.EqualFold(req.Method, http.MethodPost)) {
			return okEnvelope(managementResponse{StatusCode: http.StatusMethodNotAllowed, Headers: jsonHeaders(), Body: mustJSON(map[string]any{"error": "method_not_allowed"})})
		}
		accounts, errQuota := loadKiroQuotas(req.HostCallbackID)
		if errQuota != nil {
			return okEnvelope(managementResponse{
				StatusCode: http.StatusBadGateway,
				Headers:    jsonHeaders(),
				Body:       mustJSON(map[string]any{"error": "quota_query_failed", "message": errQuota.Error()}),
			})
		}
		return okEnvelope(managementResponse{
			StatusCode: http.StatusOK,
			Headers:    jsonHeaders(),
			Body: mustJSON(map[string]any{
				"provider":     providerID,
				"generated_at": time.Now().UTC().Format(time.RFC3339),
				"accounts":     accounts,
			}),
		})
	}
	return okEnvelope(managementResponse{
		StatusCode: http.StatusNotFound,
		Headers:    jsonHeaders(),
		Body:       mustJSON(map[string]any{"error": "not_found", "path": path}),
	})
}
