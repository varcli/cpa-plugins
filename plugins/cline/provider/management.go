package provider

// management.go registers the plugin's management API and web panel.
//
// Routes are exposed under two prefixes:
//   - /v0/management/plugins/cline/*  — JSON endpoints, authenticated by the
//     host's management middleware.
//   - /v0/resource/plugins/cline/*    — the unauthenticated browser UI
//     (panel.html) plus the sidebar menu entry.
//
// The contract matches the other in-tree provider plugins: handleManagement
// returns an envelope-wrapped pluginapi.ManagementResponse, and the host writes
// only resp.Body to the browser, so JSON.parse / HTML rendering work unchanged.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/varcli/cpa-plugins/plugins/cline/clinenx"
)

var (
	managementBasePathCache = "/v0/management"
	resourceBasePathCache   = "/v0/resource/plugins/" + providerName
	managementBasePathMu    sync.RWMutex
	managementResourceMu    sync.RWMutex
)

// SetManagementBasePath records the BasePath reported by management.register so
// the panel does not hardcode /v0/management.
func SetManagementBasePath(path string) {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	if path == "" {
		return
	}
	managementBasePathMu.Lock()
	managementBasePathCache = path
	managementBasePathMu.Unlock()
}

func loadedManagementBasePath() string {
	managementBasePathMu.RLock()
	defer managementBasePathMu.RUnlock()
	return managementBasePathCache
}

// SetResourceBasePath records the resource prefix the host assigned, tolerating
// a future host path change.
func SetResourceBasePath(path string) {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	if path == "" {
		return
	}
	managementResourceMu.Lock()
	resourceBasePathCache = path
	managementResourceMu.Unlock()
}

func loadedResourceBasePath() string {
	managementResourceMu.RLock()
	defer managementResourceMu.RUnlock()
	return resourceBasePathCache
}

// managementRegistration describes the routes and resources this plugin serves.
// Paths are relative to /plugins/cline — the host prepends /v0/management for
// Routes and /v0/resource/plugins/cline for Resources.
func managementRegistration() managementRegistrationResponse {
	base := "/plugins/" + providerName
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: base + "/models", Description: "List the effective Cline model catalog with groups and source."},
			{Method: http.MethodPut, Path: base + "/models", Description: "Replace the model overlay (hide/order/add)."},
			{Method: http.MethodPost, Path: base + "/models/action", Description: "Apply one model action: hide, restore, move, add."},
			{Method: http.MethodGet, Path: base + "/accounts", Description: "List Cline accounts and subscription status."},
			{Method: http.MethodPost, Path: base + "/oauth/start", Description: "Start a Cline WorkOS device login."},
			{Method: http.MethodPost, Path: base + "/oauth/poll", Description: "Poll a Cline WorkOS device login."},
			{Method: http.MethodPost, Path: base + "/accounts/rename", Description: "Rename one account (body: {id, name})."},
			{Method: http.MethodPost, Path: base + "/accounts/delete", Description: "Delete one account (body: {id})."},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "Cline", Description: "Cline/ClinePass account and model dashboard."},
		},
	}
}

// handleManagement dispatches one ManagementRequest to the right handler and
// returns an envelope-wrapped pluginapi.ManagementResponse.
func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequestWire
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode Cline management request: %w", err)
	}
	path := strings.TrimRight(strings.TrimSpace(req.Path), "/")

	// Browser UI resource routes (unauthenticated).
	resPrefix := loadedResourceBasePath()
	if req.Method == http.MethodGet && strings.HasPrefix(path, resPrefix) {
		sub := strings.TrimPrefix(path, resPrefix)
		if sub != "" && sub != "/" && sub != "/panel" && sub != "/panel.html" {
			return okEnvelope(mgmtHTMLResponse(http.StatusNotFound, []byte("<h1>404</h1>")))
		}
		return okEnvelope(mgmtHTMLResponse(http.StatusOK, servePanel()))
	}

	// JSON API routes (host-authenticated).
	base := loadedManagementBasePath() + "/plugins/" + providerName
	switch {
	case req.Method == http.MethodGet && path == base+"/models":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, modelCatalogResponse(req.Query.Get("refresh") != "")))
	case req.Method == http.MethodPut && path == base+"/models":
		var overlay modelOverlay
		if err := json.Unmarshal(req.Body, &overlay); err != nil {
			return okEnvelope(mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "invalid overlay"}))
		}
		state, err := storeModelOverlay(overlay)
		if err != nil {
			return okEnvelope(mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": err.Error()}))
		}
		return okEnvelope(mgmtJSONResponse(http.StatusOK, modelCatalogResponseWithState(state, false)))
	case req.Method == http.MethodPost && path == base+"/models/action":
		return okEnvelope(handleModelAction(req.Body))
	case req.Method == http.MethodGet && path == base+"/accounts":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, accountSummary()))
	case req.Method == http.MethodPost && path == base+"/oauth/start":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, managementOAuthStart()))
	case req.Method == http.MethodPost && path == base+"/oauth/poll":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, managementOAuthPoll(req.ManagementRequest)))
	case req.Method == http.MethodPost && path == base+"/accounts/rename":
		return okEnvelope(handleAccountRename(req.Body))
	case req.Method == http.MethodPost && path == base+"/accounts/delete":
		return okEnvelope(handleAccountDelete(req.Body))
	default:
		return okEnvelope(mgmtJSONResponse(http.StatusNotFound, map[string]any{"error": "not found: " + path}))
	}
}

// managementOAuthStart opens a WorkOS device login for the panel.
//
// The panel drives start/poll through these plugin-owned routes rather than the
// host's generic auth-login route, because the device grant needs the plugin's
// own state token back for polling.
func managementOAuthStart() map[string]any {
	resp, err := beginLogin()
	if err != nil {
		return map[string]any{"status": "error", "error": err.Error()}
	}
	return map[string]any{
		"status":     "pending",
		"url":        resp.URL,
		"state":      resp.State,
		"expires_at": resp.ExpiresAt,
	}
}

// managementOAuthPoll drives one poll and persists the credential through
// host.auth.save when the browser grant lands. It answers "ok" on success so the
// panel does not have to know the host auth-provider status vocabulary.
func managementOAuthPoll(req pluginapi.ManagementRequest) map[string]any {
	state := strings.TrimSpace(req.Query.Get("state"))
	if state == "" && len(req.Body) > 0 {
		var body struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(req.Body, &body); err == nil {
			state = strings.TrimSpace(body.State)
		}
	}
	if state == "" {
		return map[string]any{"status": "error", "error": "state is required"}
	}
	resp := pollLoginState(state)
	switch resp.Status {
	case pluginapi.AuthLoginStatusPending:
		return map[string]any{"status": "pending", "message": resp.Message}
	case pluginapi.AuthLoginStatusSuccess:
		if err := persistAuthData(resp.Auth); err != nil {
			return map[string]any{"status": "error", "error": err.Error()}
		}
		return map[string]any{
			"status": "ok",
			"file":   resp.Auth.FileName,
			"label":  resp.Auth.Label,
		}
	default:
		message := resp.Message
		if message == "" {
			message = "login failed"
		}
		return map[string]any{"status": "error", "error": message}
	}
}

// handleAccountRename stores a display name for one credential. The value is
// persisted under account.nickname and mirrored to the top-level label, which is
// the field CPA's native auth list renders.
func handleAccountRename(body []byte) pluginapi.ManagementResponse {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "invalid body"})
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "id is required"})
	}
	file, raw, err := findOwnAuthFile(req.ID)
	if err != nil {
		return mgmtJSONResponse(http.StatusNotFound, map[string]any{"error": err.Error()})
	}
	sa, err := parseStored(raw)
	if err != nil {
		return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "stored auth is not a Cline credential"})
	}
	nickname := strings.TrimSpace(req.Name)
	if nickname == "" {
		nickname = firstNonEmpty(sa.Account.DisplayName, sa.Account.Email, providerName)
	}
	if err := setAuthFileNickname(effectiveAuthName(file), raw, nickname); err != nil {
		return mgmtJSONResponse(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	return mgmtJSONResponse(http.StatusOK, map[string]any{
		"status":   "ok",
		"file":     effectiveAuthName(file),
		"nickname": nickname,
	})
}

// handleAccountDelete removes one credential.
//
// The plugin ABI has no auth.delete method, so this hands the panel the host's
// own auth-files route to call; the panel is same-origin with CPA and already
// holds the management key.
func handleAccountDelete(body []byte) pluginapi.ManagementResponse {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "invalid body"})
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "id is required"})
	}
	file, _, err := findOwnAuthFile(req.ID)
	if err != nil {
		return mgmtJSONResponse(http.StatusNotFound, map[string]any{"error": err.Error()})
	}
	name := effectiveAuthName(file)
	return mgmtJSONResponse(http.StatusOK, map[string]any{
		"status":  "ok",
		"file":    name,
		"method":  http.MethodDelete,
		"url":     loadedManagementBasePath() + "/auth-files?name=" + url.QueryEscape(name),
		"message": "credential removed; re-login from the panel to add it back",
	})
}

// modelCatalogResponseWire is the panel's catalog payload.
//
// RefreshError reports the outcome of a forced pull. A failed pull still answers
// 200 with the previous catalog on purpose (blanking the section would be
// worse), so without this field the panel cannot tell "upstream had nothing new"
// from "the pull failed" and the refresh button silently lies.
type modelCatalogResponseWire struct {
	Provider     string                `json:"provider"`
	Source       string                `json:"source"`
	Groups       map[string][]string   `json:"groups"`
	Models       []pluginapi.ModelInfo `json:"models"`
	Overlay      modelOverlayState     `json:"overlay"`
	RefreshError string                `json:"refresh_error,omitempty"`
}

func modelCatalogResponse(force bool) modelCatalogResponseWire {
	return modelCatalogResponseWithState(loadedModelOverlayState(), force)
}

func modelCatalogResponseWithState(state modelOverlayState, force bool) modelCatalogResponseWire {
	base, groups, source, refreshErr := baseModelCatalogForce(force)
	models := applyModelOverlayForAdmin(base, state.Overlay)
	resp := modelCatalogResponseWire{
		Provider: providerName,
		Source:   source,
		Groups:   groups,
		Models:   models,
		Overlay:  state,
	}
	if refreshErr != nil {
		resp.RefreshError = refreshErr.Error()
	}
	return resp
}

type modelActionRequest struct {
	Action string `json:"action"`
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Before string `json:"before,omitempty"`
}

// handleModelAction applies one panel edit to the overlay and answers with the
// refreshed admin catalog so the panel re-renders from server state rather than
// from its own guess.
func handleModelAction(raw []byte) pluginapi.ManagementResponse {
	var req modelActionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "invalid action request"})
	}
	state := loadedModelOverlayState()
	overlay := state.Overlay
	id := strings.TrimSpace(req.ID)
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "hide":
		if id == "" {
			return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "id is required"})
		}
		overlay.Hide = clinenx.AppendUnique(overlay.Hide, id)
		overlay.Order = clinenx.RemoveString(overlay.Order, id)
	case "restore":
		overlay.Hide = clinenx.RemoveString(overlay.Hide, id)
	case "add":
		if id == "" {
			return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "id is required"})
		}
		if _, exists := modelInfoByID(baseModelCatalogOnly(), id); !exists {
			overlay.Add = clinenx.AppendUnique(overlay.Add, id)
		}
		overlay.Hide = clinenx.RemoveString(overlay.Hide, id)
	case "move":
		overlay.Order = clinenx.MoveBefore(overlay.Order, id, req.Before)
	default:
		return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "unknown action"})
	}
	next, err := storeModelOverlay(overlay)
	if err != nil {
		return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": err.Error()})
	}
	return mgmtJSONResponse(http.StatusOK, modelCatalogResponseWithState(next, false))
}

func baseModelCatalogOnly() []pluginapi.ModelInfo {
	models, _, _ := baseModelCatalog()
	return models
}

type accountSummaryWire struct {
	Accounts []accountSummaryEntry `json:"accounts"`
	Error    string                `json:"error,omitempty"`
}

type accountSummaryEntry struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Nickname    string `json:"nickname,omitempty"`
	Email       string `json:"email,omitempty"`
	Plan        string `json:"plan,omitempty"`
	PlanStatus  string `json:"plan_status,omitempty"`
	LastChecked string `json:"last_checked,omitempty"`
	File        string `json:"file,omitempty"`
	Note        string `json:"note,omitempty"`
}

// accountSummary lists this plugin's credentials for the panel.
//
// The listing is deliberately read-only: it reports what the host store holds,
// and any change goes through the rename/delete routes so a failure is visible
// to the operator instead of being swallowed.
func accountSummary() accountSummaryWire {
	files, err := hostAuthListFiles()
	if err != nil {
		return accountSummaryWire{Error: "host auth list unavailable"}
	}
	result := accountSummaryWire{}
	for _, file := range files {
		if !isOwnAuthFile(file) {
			continue
		}
		raw, errGet := hostAuthGetByIndex(file.AuthIndex)
		if errGet != nil {
			continue
		}
		sa, errParse := parseStored(raw)
		if errParse != nil {
			continue
		}
		result.Accounts = append(result.Accounts, accountSummaryEntry{
			ID:          file.AuthIndex,
			Label:       clineAccountLabel(sa),
			Nickname:    sa.Account.Nickname,
			Email:       sa.Account.Email,
			Plan:        sa.Account.Plan,
			PlanStatus:  sa.Account.PlanStatus,
			LastChecked: time.Now().UTC().Format(time.RFC3339),
			File:        effectiveAuthName(file),
			Note:        noteFromAuthFile(raw),
		})
	}
	return result
}

func mgmtJSONResponse(status int, value any) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    jsonHeaders(),
		Body:       mustJSON(value),
	}
}

func mgmtHTMLResponse(status int, value []byte) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: value,
	}
}
