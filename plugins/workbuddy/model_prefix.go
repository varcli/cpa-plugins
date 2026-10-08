// model_prefix.go — v0.13.0: every model id this plugin registers carries a
// literal "<provider>/" prefix (workbuddy/…), so CPA groups the models under
// the workbuddy provider and the ids can never collide with another plugin
// advertising the same upstream model name. This is the repository rule for
// provider plugins (register with a model prefix plus a config toggle) and
// matches the form kiro already uses ("kiro/CodeBuddy-sonnet-4.5").
//
// The prefix is applied at the SERVE boundary (handleModelForAuth / the
// models/groups picker), never inside the discovery assembly: the merge and
// learned-alias code keys on bare upstream ids, and a raw (unprefixed)
// snapshot on disk means the change-guard and every serve branch keep working
// unchanged — addModelPrefix is idempotent, so re-applying is free.
//
// The host has NO mechanism that strips a literal plugin prefix (it only
// strips the operator-managed per-credential auth.Prefix), so the prefixed id
// the host dispatches reaches the executor verbatim — stripModelPrefix runs on
// it in resolveUpstreamModel before the upstream call.
package main

import (
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// defaultModelPrefix is applied when the operator does not override
// model_prefix. Derived from the provider id so the plugin id and the model
// namespace can never drift apart.
var defaultModelPrefix = providerName + "/"

var (
	modelPrefixMu sync.RWMutex
	// modelPrefixValue is always normalized to a trailing-slash form.
	modelPrefixValue = defaultModelPrefix
	// enableModelPrefixValue mirrors the enable_model_prefix toggle.
	enableModelPrefixValue = true
)

// setModelPrefixConfig applies the model_prefix / enable_model_prefix plugin
// config keys. An absent or empty model_prefix falls back to the default; a
// missing trailing slash is added. enabled=false turns the prefix off entirely
// (bare ids), so this plugin can be pinned to the upstream-native namespace.
func setModelPrefixConfig(prefix string, enabled bool) {
	p := strings.TrimSpace(prefix)
	if p == "" {
		p = defaultModelPrefix
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	modelPrefixMu.Lock()
	modelPrefixValue = p
	enableModelPrefixValue = enabled
	modelPrefixMu.Unlock()
}

// modelPrefix returns the effective prefix ("workbuddy/") or "" when the
// toggle is off.
func modelPrefix() string {
	modelPrefixMu.RLock()
	defer modelPrefixMu.RUnlock()
	if !enableModelPrefixValue {
		return ""
	}
	return modelPrefixValue
}

// addModelPrefix prefixes one advertised model id. Idempotent: an id that
// already carries the effective prefix (or the built-in default, so a
// snapshot persisted before a prefix change is not double-prefixed) passes
// through untouched. Ids are never split on "/" — upstream ids may legitimately
// contain a namespace segment, and both operations are exact-string.
func addModelPrefix(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return id
	}
	p := modelPrefix()
	if p == "" {
		return id
	}
	if strings.HasPrefix(id, p) || strings.HasPrefix(id, defaultModelPrefix) {
		return id
	}
	return p + id
}

// prefixModelInfos applies the advertised model prefix to a whole catalog,
// preserving every other field.
func prefixModelInfos(in []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	out := make([]pluginapi.ModelInfo, 0, len(in))
	for _, m := range in {
		m.ID = addModelPrefix(m.ID)
		out = append(out, m)
	}
	return out
}

// stripModelPrefix removes this plugin's own prefix before an id is forwarded
// upstream. Both the configured prefix and the built-in default are candidates,
// so ids the host registered while the prefix was on still reach upstream bare
// after the operator changes or disables it. Bare or foreign ids pass through.
func stripModelPrefix(model string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return m
	}
	modelPrefixMu.RLock()
	configured := modelPrefixValue
	modelPrefixMu.RUnlock()
	candidates := []string{configured}
	if configured != defaultModelPrefix {
		candidates = append(candidates, defaultModelPrefix)
	}
	for _, p := range candidates {
		if p != "" && strings.HasPrefix(m, p) {
			return strings.TrimPrefix(m, p)
		}
	}
	return m
}
