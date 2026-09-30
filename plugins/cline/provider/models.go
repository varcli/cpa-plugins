package provider

// models.go owns the model catalog: live discovery from Cline's
// recommended-models feed, the per-account cache, the merge across accounts, and
// the built-in fallback used before any account has been pulled.
//
// Advertised ids carry this plugin's model prefix (see modelPrefix) so the
// plugin can coexist with another Cline provider in the same host. The prefix is
// applied when the catalog is built, which means the overlay, the panel and the
// registered model list all speak the same prefixed ids the executor later
// strips before forwarding upstream.

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	staticModelGroup = "default"
	freeModelGroup   = "free"
	passModelGroup   = "clinepass"
	cloudModelGroup  = "clinecloud"
)

var (
	modelCacheMu sync.RWMutex
	modelCache   = map[string]cachedModelCatalog{}
)

// fallbackModels is the catalog served before any account has been discovered.
// It is a snapshot of what Cline's feed published when this list was last
// reviewed; live discovery replaces it as soon as one account answers.
func fallbackModels() []pluginapi.ModelInfo {
	return []pluginapi.ModelInfo{
		modelInfo("cline-pass/deepseek-v4.1-flash", "ClinePass DeepSeek V4.1 Flash", passModelGroup, "Fast ClinePass model with a 1M context window."),
		modelInfo("cline-pass/glm-5.3", "ClinePass GLM 5.3", passModelGroup, "Z-AI top open-weights coding model."),
		modelInfo("cline-pass/glm-5.2", "ClinePass GLM 5.2", passModelGroup, "Z-AI GLM-5.2 coding model."),
		modelInfo("cline-pass/deepseek-v4-pro", "ClinePass DeepSeek V4 Pro", passModelGroup, "Frontier reasoning and coding with 1M context."),
		modelInfo("cline-pass/deepseek-v4-flash", "ClinePass DeepSeek V4 Flash", passModelGroup, "Fast DeepSeek coding model."),
		modelInfo("cline-pass/kimi-k3", "ClinePass Kimi K3", passModelGroup, "Moonshot flagship open-weights model."),
		modelInfo("cline-pass/kimi-k2.7-code", "ClinePass Kimi K2.7 Code", passModelGroup, "Moonshot code-focused model."),
		modelInfo("cline-pass/kimi-k2.6", "ClinePass Kimi K2.6", passModelGroup, "Moonshot open-weights coding model."),
		modelInfo("cline-pass/qwen3.8-max", "ClinePass Qwen3.8 Max", passModelGroup, "Qwen SOTA coding model."),
		modelInfo("cline-free/kimi-k3", "Cline Free Kimi K3", freeModelGroup, "Free Moonshot flagship model."),
		modelInfo("cline-free/deepseek-v4.1-flash", "Cline Free DeepSeek V4.1 Flash", freeModelGroup, "Free Cline model with a 1M context window."),
		modelInfo("cline-free/muse-spark-1.3-contributor", "Cline Free Muse Spark 1.3 Contributor", freeModelGroup, "Meta multimodal reasoning model."),
		modelInfo("z-ai/glm-5.3-flash", "Cline Free GLM 5.3 Flash", freeModelGroup, "Latest multimodal GLM-5 model."),
		modelInfo("cline-free/solar-pro4", "Cline Free Solar Pro 4", freeModelGroup, "Document and coding model."),
		modelInfo("poolside/laguna-s-2.1:free", "Cline Free Laguna S 2.1", freeModelGroup, "Poolside coding agent model."),
		modelInfo("openai/gpt-6-astra", "Cline Recommended GPT-6 Astra", staticModelGroup, "Frontier OpenAI model through Cline."),
		modelInfo("moonshotai/kimi-k3", "Cline Recommended Kimi K3", staticModelGroup, "Moonshot flagship model."),
		modelInfo("Tencent/CodeBuddy-opus-5", "Cline Recommended CodeBuddy Opus 5", staticModelGroup, "Tencent frontier model."),
		modelInfo("x-ai/grok-4.5", "Cline Recommended Grok 4.5", staticModelGroup, "xAI frontier model."),
	}
}

// clientCompatibilityModels are ids shipped or observed in Cline Desktop that
// have been absent from the recommended-models feed at times. Keeping them as a
// union with live discovery means a feed omission cannot remove a usable model.
func clientCompatibilityModels() []pluginapi.ModelInfo {
	return []pluginapi.ModelInfo{
		modelInfo("cline-pass/glm-5.2", "ClinePass GLM 5.2", passModelGroup, "Z-AI GLM-5.2 coding model."),
		modelInfo("cline-pass/deepseek-v4-flash", "ClinePass DeepSeek V4 Flash", passModelGroup, "Fast DeepSeek coding model."),
		modelInfo("cline-pass/kimi-k2.7-code", "ClinePass Kimi K2.7 Code", passModelGroup, "Moonshot code-focused model."),
		modelInfo("cline-pass/kimi-k2.6", "ClinePass Kimi K2.6", passModelGroup, "Moonshot open-weights coding model."),
		modelInfo("cline-free/kimi-k3", "Cline Free Kimi K3", freeModelGroup, "Free Moonshot flagship model."),
	}
}

// modelInfo renders one catalog entry, applying this plugin's model prefix so
// the advertised id is the one CPA registers.
func modelInfo(id, name, group, description string) pluginapi.ModelInfo {
	advertised := strings.TrimSpace(modelPrefix() + strings.TrimSpace(id))
	return pluginapi.ModelInfo{
		ID:                         advertised,
		Object:                     "model",
		OwnedBy:                    providerID,
		Type:                       "chat",
		DisplayName:                name,
		Name:                       name,
		Description:                description,
		ContextLength:              1048576,
		MaxCompletionTokens:        8192,
		SupportedGenerationMethods: []string{"chat"},
		SupportedParameters:        []string{"tools", "reasoning"},
		SupportedInputModalities:   []string{"text"},
		SupportedOutputModalities:  []string{"text"},
	}
}

// defaultModelInfo renders an overlay-added entry. Its id is already final (the
// operator typed it into the panel), so no prefix is applied.
func defaultModelInfo(id, name string) pluginapi.ModelInfo {
	return pluginapi.ModelInfo{
		ID:                         strings.TrimSpace(id),
		Object:                     "model",
		OwnedBy:                    providerID,
		Type:                       "chat",
		DisplayName:                strings.TrimSpace(name),
		Name:                       strings.TrimSpace(name),
		ContextLength:              1048576,
		MaxCompletionTokens:        8192,
		SupportedGenerationMethods: []string{"chat"},
		SupportedParameters:        []string{"tools", "reasoning"},
		SupportedInputModalities:   []string{"text"},
		SupportedOutputModalities:  []string{"text"},
	}
}

// -----------------------------------------------------------------------------
// RPC entry points
// -----------------------------------------------------------------------------

// modelsStatic answers model.static.
func modelsStatic(raw []byte) ([]byte, error) {
	applyConfig(raw)
	models, _, _ := effectiveModelCatalogWithState()
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}

// modelsForAuth answers model.for_auth.
func modelsForAuth(raw []byte) ([]byte, error) {
	var req authModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		models, _, _ := effectiveModelCatalogWithState()
		return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
	}
	// Discovery runs on the host's schedule, not the user's, so it is the path
	// most likely to meet an expired token: an hour after login the catalog would
	// silently drop out of the panel without this.
	models, _, _ := modelCatalogForAuth(freshStoredAuth(sa, req.Attributes))
	// The per-auth catalog is cached unfiltered; apply the overlay (including
	// config hidden_models) at read time so config changes take effect without
	// waiting out the cache TTL.
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: applyModelOverlay(models, loadedModelOverlay())})
}

// -----------------------------------------------------------------------------
// Catalog assembly
// -----------------------------------------------------------------------------

func effectiveModelCatalogWithState() ([]pluginapi.ModelInfo, map[string][]string, string) {
	overlay := loadedModelOverlay()
	base, groups, source := baseModelCatalog()
	return applyModelOverlay(base, overlay), groups, source
}

func baseModelCatalog() ([]pluginapi.ModelInfo, map[string][]string, string) {
	models, groups, source, _ := baseModelCatalogForce(false)
	return models, groups, source
}

// baseModelCatalogForce serves the merged per-account catalog, which is what CPA
// registers, and only falls back to the built-in list when no account has a
// catalog yet — for example straight after a restart, before any client pulled
// one.
func baseModelCatalogForce(force bool) ([]pluginapi.ModelInfo, map[string][]string, string, error) {
	accounts, err := ownStoredAuths()
	if err != nil {
		return configuredModels(fallbackModels()), fallbackGroups(), "fallback", err
	}
	models, groups, source, refreshErr, ok := mergeStoredAuthCatalogsWithError(accounts, force)
	if !ok {
		return configuredModels(fallbackModels()), fallbackGroups(), "fallback", refreshErr
	}
	return configuredModels(models), groups, source, refreshErr
}

// configuredModels appends the operator's `models` config entries to a catalog.
//
// The key exists for the model Cline publishes to a client but not to the
// recommended-models feed; without it the operator has no way to reach such a
// model short of a panel "add", which does not survive a restart. Entries are
// read as Cline-native ids and take the plugin's prefix like the rest of the
// catalog, so the advertised id and the id the executor forwards upstream stay
// consistent. The overlay is applied afterwards, so a configured model can still
// be hidden or pinned.
func configuredModels(base []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	extra := loadedConfig().Models
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	for _, model := range base {
		seen[strings.TrimSpace(model.ID)] = struct{}{}
	}
	out := base
	for _, raw := range extra {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		model := modelInfo(id, id, staticModelGroup, "Configured Cline model.")
		if _, dup := seen[model.ID]; dup {
			continue
		}
		seen[model.ID] = struct{}{}
		out = append(out, model)
	}
	return out
}

// mergeStoredAuthCatalogsWithError folds the account catalogs into the list CPA
// serves and reports whether any account failed to refresh, so a forced pull can
// tell the caller the truth while still returning a usable list.
func mergeStoredAuthCatalogsWithError(accounts []*storedAuth, force bool) ([]pluginapi.ModelInfo, map[string][]string, string, error, bool) {
	merger := newCatalogMerger()
	pulled := 0
	var refreshErr error
	for _, sa := range accounts {
		models, groups, source, err, ok := accountCatalogForMergeWithError(sa, force)
		if err != nil && refreshErr == nil {
			refreshErr = err
		}
		if !ok {
			continue
		}
		pulled++
		merger.add(models, groups, source)
	}
	merged, groups, source, ok := merger.result(pulled)
	return merged, groups, source, refreshErr, ok
}

// catalogMerger folds per-account catalogs into the single list CPA serves.
//
// Several accounts usually offer the same model, so both the models and the
// group memberships have to dedupe by ID; without that the panel shows the same
// ID once per account and a group lists a member twice.
type catalogMerger struct {
	seen      map[string]struct{}
	merged    []pluginapi.ModelInfo
	groups    map[string][]string
	groupSeen map[string]map[string]struct{}
	source    string
}

func newCatalogMerger() *catalogMerger {
	return &catalogMerger{
		seen:      make(map[string]struct{}),
		merged:    make([]pluginapi.ModelInfo, 0, 32),
		groups:    make(map[string][]string),
		groupSeen: make(map[string]map[string]struct{}),
	}
}

func (m *catalogMerger) add(models []pluginapi.ModelInfo, groups map[string][]string, source string) {
	// Any account backed by the real upstream catalog is enough to call the
	// merged list upstream-sourced. "fallback" means that account could not
	// fetch, so it must never outrank a real one.
	if m.source == "" || m.source == "fallback" {
		if source != "" && source != "fallback" {
			m.source = source
		} else if source == "fallback" && m.source == "" {
			m.source = source
		}
	}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if _, dup := m.seen[id]; dup {
			continue
		}
		m.seen[id] = struct{}{}
		m.merged = append(m.merged, model)
	}
	for group, ids := range groups {
		members, has := m.groupSeen[group]
		if !has {
			members = make(map[string]struct{}, len(ids))
			m.groupSeen[group] = members
		}
		for _, id := range ids {
			if _, dup := members[id]; dup {
				continue
			}
			members[id] = struct{}{}
			m.groups[group] = append(m.groups[group], id)
		}
	}
}

// result reports ok when at least one account contributed a model. pulled is
// counted by the caller because an account can be present but contribute nothing.
func (m *catalogMerger) result(pulled int) ([]pluginapi.ModelInfo, map[string][]string, string, bool) {
	if pulled == 0 || len(m.merged) == 0 {
		return nil, nil, "", false
	}
	if m.source == "" {
		m.source = "fallback"
	}
	return m.merged, m.groups, m.source, true
}

// accountCatalogForMergeWithError picks one account's catalog for the merge and
// also returns the refresh error.
//
// A warm cache is served as is. A cold or expired one is refreshed here rather
// than abandoned: returning ok=false on a miss meant that after every CPA
// restart, and for the whole first model listing of a fresh process, the merge
// found no contributor and the caller silently served the built-in fallback
// list. That is the "the plugin never picks up new upstream models" report: the
// cache is process-local, so a restart emptied it and upstream discovery never
// ran unless somebody pressed the panel's refresh button.
//
// force is still honoured separately: it ignores a warm cache so the refresh
// button cannot be swallowed by the TTL.
func accountCatalogForMergeWithError(sa *storedAuth, force bool) ([]pluginapi.ModelInfo, map[string][]string, string, error, bool) {
	if !force {
		modelCacheMu.RLock()
		cached, ok := modelCache[accountCacheKey(sa)]
		modelCacheMu.RUnlock()
		if ok && time.Since(cached.FetchedAt) < modelCacheTTL {
			// Clone so a merge can never hand the caller an alias of the cache.
			return cloneModelInfos(cached.Models), cloneGroups(cached.Groups), cached.Source, nil, true
		}
	}
	models, groups, source, err := refreshModelCatalog(freshStoredAuth(sa, nil))
	return models, groups, source, err, len(models) > 0
}

// modelCatalogForAuth returns one account's catalog for the panel's per-auth
// view, refreshing it when the cache is cold.
func modelCatalogForAuth(sa *storedAuth) ([]pluginapi.ModelInfo, map[string][]string, string) {
	key := accountCacheKey(sa)
	modelCacheMu.RLock()
	cached, ok := modelCache[key]
	modelCacheMu.RUnlock()
	if ok && time.Since(cached.FetchedAt) < modelCacheTTL {
		return cloneModelInfos(cached.Models), cloneGroups(cached.Groups), cached.Source
	}
	models, groups, source, _ := refreshModelCatalog(sa)
	return models, groups, source
}

// refreshModelCatalog pulls the catalog and caches it.
//
// A failed pull keeps whatever was cached before rather than storing the
// built-in fallback: an operator pressing refresh during a brief upstream hiccup
// would otherwise serve the wrong list for a full TTL, which is the failure mode
// this whole path is meant to remove. The error is still returned so an admin
// call can report it, and callers that only need a usable list may ignore it.
func refreshModelCatalog(sa *storedAuth) ([]pluginapi.ModelInfo, map[string][]string, string, error) {
	key := accountCacheKey(sa)
	models, groups, source, entitlement, err := fetchRecommendedModels(sa)
	if err != nil {
		modelCacheMu.RLock()
		previous, had := modelCache[key]
		modelCacheMu.RUnlock()
		if had && len(previous.Models) > 0 {
			log.Printf("cline: refresh failed for %s, keeping the previous catalog: %v", key, err)
			return cloneModelInfos(previous.Models), cloneGroups(previous.Groups), previous.Source, err
		}
		models, groups, source = fallbackModels(), fallbackGroups(), "fallback"
	}
	modelCacheMu.Lock()
	modelCache[key] = cachedModelCatalog{
		Models:      cloneModelInfos(models),
		Groups:      cloneGroups(groups),
		FetchedAt:   time.Now(),
		Source:      source,
		Entitlement: entitlement,
	}
	modelCacheMu.Unlock()
	return models, groups, source, err
}

// accountCacheKey identifies one account's cache slot.
//
// The prefix is part of the key: toggling enable_model_prefix changes every
// advertised id, and a cache entry keyed only by account would keep serving the
// old shape for a full TTL after the operator flipped the switch.
func accountCacheKey(sa *storedAuth) string {
	base := "global"
	if sa != nil {
		switch {
		case sa.Account.ID != "":
			base = sa.Account.ID
		case sa.Account.Email != "":
			base = strings.ToLower(sa.Account.Email)
		}
	}
	return base + "|" + modelPrefix()
}

// fetchRecommendedModels pulls Cline's recommended-models feed for one account
// through the host HTTP bridge.
func fetchRecommendedModels(sa *storedAuth) ([]pluginapi.ModelInfo, map[string][]string, string, string, error) {
	if sa == nil || strings.TrimSpace(sa.Auth.AccessToken) == "" {
		return nil, nil, "", "", fmt.Errorf("cline credential is missing")
	}
	response, err := clineGet(clineAPIBase+"/api/v1/ai/cline/recommended-models", clineHeaders(sa.Auth.AccessToken))
	if err != nil {
		return nil, nil, "", "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, "", "", &upstreamStatusError{
			status:  response.StatusCode,
			message: fmt.Sprintf("recommended models HTTP %d", response.StatusCode),
		}
	}
	var parsed recommendedModels
	if err := json.Unmarshal(response.Body, &parsed); err != nil {
		return nil, nil, "", "", fmt.Errorf("decode recommended models: %w", err)
	}
	models := make([]pluginapi.ModelInfo, 0, len(parsed.Recommended)+len(parsed.Free)+len(parsed.ClinePass)+len(parsed.ClineCloud))
	groups := map[string][]string{}
	appendGroup := func(group string, entries []recommendedModel) {
		for _, entry := range entries {
			if entry.ID == "" {
				continue
			}
			model := modelInfo(entry.ID, entry.Name, group, entry.Description)
			models = append(models, model)
			groups[group] = append(groups[group], model.ID)
		}
	}
	appendGroup(staticModelGroup, parsed.Recommended)
	appendGroup(freeModelGroup, parsed.Free)
	appendGroup(passModelGroup, parsed.ClinePass)
	appendGroup(cloudModelGroup, parsed.ClineCloud)
	if len(models) == 0 {
		return nil, nil, "", "", fmt.Errorf("recommended models response is empty")
	}
	models, groups = mergeModelCatalog(models, groups, clientCompatibilityModels(), clientCompatibilityGroups())
	entitlement := "unknown"
	if len(parsed.ClinePass) > 0 {
		entitlement = "listed"
	}
	return models, groups, "cline-recommended", entitlement, nil
}

func fallbackGroups() map[string][]string {
	prefix := modelPrefix()
	withPrefix := func(ids ...string) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			out = append(out, prefix+id)
		}
		return out
	}
	return map[string][]string{
		staticModelGroup: withPrefix(
			"openai/gpt-6-astra",
			"moonshotai/kimi-k3",
			"Tencent/CodeBuddy-opus-5",
			"x-ai/grok-4.5",
		),
		freeModelGroup: withPrefix(
			"cline-free/kimi-k3",
			"cline-free/deepseek-v4.1-flash",
			"cline-free/muse-spark-1.3-contributor",
			"z-ai/glm-5.3-flash",
			"cline-free/solar-pro4",
			"poolside/laguna-s-2.1:free",
		),
		passModelGroup: withPrefix(
			"cline-pass/deepseek-v4.1-flash",
			"cline-pass/glm-5.3",
			"cline-pass/glm-5.2",
			"cline-pass/deepseek-v4-pro",
			"cline-pass/deepseek-v4-flash",
			"cline-pass/kimi-k3",
			"cline-pass/kimi-k2.7-code",
			"cline-pass/kimi-k2.6",
			"cline-pass/qwen3.8-max",
		),
	}
}

func clientCompatibilityGroups() map[string][]string {
	prefix := modelPrefix()
	return map[string][]string{
		freeModelGroup: {
			prefix + "cline-free/kimi-k3",
		},
		passModelGroup: {
			prefix + "cline-pass/glm-5.2",
			prefix + "cline-pass/deepseek-v4-flash",
			prefix + "cline-pass/kimi-k2.7-code",
			prefix + "cline-pass/kimi-k2.6",
		},
	}
}

// mergeModelCatalog unions the discovered catalog with the client-compatibility
// list, deduping both the models and the group memberships.
func mergeModelCatalog(primary []pluginapi.ModelInfo, primaryGroups map[string][]string, required []pluginapi.ModelInfo, requiredGroups map[string][]string) ([]pluginapi.ModelInfo, map[string][]string) {
	models := make([]pluginapi.ModelInfo, 0, len(primary)+len(required))
	groups := map[string][]string{}
	seenModels := map[string]struct{}{}
	modelGroups := map[string]string{}
	seenGroupModels := map[string]map[string]struct{}{}

	groupForModel := func(source map[string][]string, id string) string {
		for group, ids := range source {
			for _, candidate := range ids {
				if strings.TrimSpace(candidate) == id {
					return group
				}
			}
		}
		return ""
	}
	appendGroupModel := func(group, id string) {
		if group == "" {
			return
		}
		if seenGroupModels[group] == nil {
			seenGroupModels[group] = map[string]struct{}{}
		}
		if _, ok := seenGroupModels[group][id]; ok {
			return
		}
		seenGroupModels[group][id] = struct{}{}
		groups[group] = append(groups[group], id)
	}
	appendCatalog := func(source []pluginapi.ModelInfo, sourceGroups map[string][]string) {
		for _, model := range source {
			id := strings.TrimSpace(model.ID)
			if id == "" {
				continue
			}
			group := groupForModel(sourceGroups, id)
			if _, ok := seenModels[id]; ok {
				if modelGroups[id] == "" {
					appendGroupModel(group, id)
					modelGroups[id] = group
				}
				continue
			}
			model.ID = id
			seenModels[id] = struct{}{}
			modelGroups[id] = group
			models = append(models, model)
			appendGroupModel(group, id)
		}
	}
	appendCatalog(primary, primaryGroups)
	appendCatalog(required, requiredGroups)
	return models, groups
}

func cloneGroups(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func cloneModelInfos(models []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	if models == nil {
		return nil
	}
	out := make([]pluginapi.ModelInfo, len(models))
	for i, model := range models {
		out[i] = model
		out[i].SupportedGenerationMethods = append([]string(nil), model.SupportedGenerationMethods...)
		out[i].SupportedParameters = append([]string(nil), model.SupportedParameters...)
		out[i].SupportedInputModalities = append([]string(nil), model.SupportedInputModalities...)
		out[i].SupportedOutputModalities = append([]string(nil), model.SupportedOutputModalities...)
	}
	return out
}

// modelInfoByID finds one catalog entry, used by the panel to render the
// per-model rows.
func modelInfoByID(models []pluginapi.ModelInfo, id string) (pluginapi.ModelInfo, bool) {
	id = strings.TrimSpace(id)
	for _, model := range models {
		if strings.TrimSpace(model.ID) == id {
			return model, true
		}
	}
	return pluginapi.ModelInfo{}, false
}
