// model_prefix.go — v0.13.0: every model id this plugin registers carries a
// literal "<provider>/" prefix (trae/…), so CPA groups trae's models under the
// trae provider instead of the fallback "other" bucket, and the ids can never
// collide with another plugin advertising the same upstream model name. This is
// the repository rule for provider plugins (register with a model prefix plus a
// config toggle) and matches the form kiro already uses
// ("kiro/CodeBuddy-sonnet-4.5").
//
// Why this file owns BOTH sides: the host has NO mechanism that strips a
// literal plugin prefix. It only strips auth.Prefix (the per-credential
// "prefix" field) before dispatch, and that field is operator-managed and
// usually unset. A prefixed advertised id therefore reaches the executor
// VERBATIM, so addModelPrefix covers everything advertised and
// stripModelPrefix runs immediately before each upstream request.
package main

import (
	"strings"
	"sync"

	// Both packages declare themselves as `package upstream`; alias them apart.
	intlupstream "github.com/varcli/cpa-plugins/plugins/trae/intlupstream"
	"github.com/varcli/cpa-plugins/plugins/trae/upstream"
)

// defaultModelPrefix is the prefix applied when the operator does not override
// model_prefix. Derived from the provider id so the plugin id and the model
// namespace can never drift apart.
const defaultModelPrefix = providerName + "/"

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
// (bare ids — the pre-0.13.0 behavior) so this plugin can be pinned to the
// upstream-native namespace on request.
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
	// The upstream request rewriter lives in another package and strips the
	// prefix right before the outbound call; keep it in lockstep. It gets ""
	// when the toggle is off so nothing is stripped from bare ids.
	if enabled {
		upstream.SetModelPrefix(p)
		intlupstream.SetModelPrefix(p)
	} else {
		upstream.SetModelPrefix("")
		intlupstream.SetModelPrefix("")
	}
}

// modelPrefix returns the effective prefix ("trae/") or "" when the toggle is
// off. Every advertised-id builder funnels through this.
func modelPrefix() string {
	modelPrefixMu.RLock()
	defer modelPrefixMu.RUnlock()
	if !enableModelPrefixValue {
		return ""
	}
	return modelPrefixValue
}

// addModelPrefix prefixes one advertised model id. It is idempotent: an id
// that already carries the effective prefix (or the default one, so a snapshot
// persisted before a prefix change is not double-prefixed) passes through
// untouched. Ids are NOT split on "/" — upstream config names may legitimately
// contain a namespace segment ("deepseek-ai/deepseek-v4-pro"), and prefixing
// and stripping are exact-string operations on the known prefix only.
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

// stripModelPrefix removes this plugin's own prefix before an id is forwarded
// upstream. Both the configured prefix and the built-in default are offered as
// candidates (longest match wins), so ids the host registered while the prefix
// was on still reach upstream bare even after the operator changes or disables
// it. Only our own prefix is stripped: a bare or foreign id passes through.
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
