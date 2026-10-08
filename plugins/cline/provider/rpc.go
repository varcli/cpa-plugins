// Package provider implements the Cline / ClinePass provider: WorkOS device-flow
// login, Cline token registration and single-flight refresh, live
// recommended-models discovery merged per account, a chat-completions executor
// over the host HTTP bridge, and a management API plus web panel.
//
// The package is the plugin's whole protocol surface; main.go is only the C ABI
// shim that dispatches host RPC method names into it.
package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/varcli/cpa-plugins/plugins/cline/clinerpc"
)

var (
	configValue    atomic.Value
	configFieldsMu sync.RWMutex
	configFields   []pluginapi.ConfigField
	pluginVersion  = "0.0.0"
)

// SetVersion records the plugin version reported in the registration metadata.
//
// The literal lives in main.go because the release tooling rewrites the single
// `Version: "x.y.z"` occurrence in plugins/<id>/*.go at the top level only
// (scripts/release.go); keeping it out of this subpackage is what lets
// `release.go version` find and bump it.
func SetVersion(version string) {
	if strings.TrimSpace(version) != "" {
		pluginVersion = version
	}
}

// Host bridge indirection. Every upstream call funnels through these so a test
// can substitute a stub caller, and so there is exactly one place that knows the
// host RPC method names.
var (
	hostHTTPDoCall = func(req hostHTTPRequest) (hostHTTPResponse, error) {
		return clinerpc.DoWithCaller(callHostCall, req)
	}
	hostHTTPDoStreamCall = func(req hostHTTPRequest) (hostHTTPStreamResponse, error) {
		return clinerpc.DoStreamWithCaller(callHostCall, req)
	}
	readHostHTTPStreamCall = func(streamID string) (hostHTTPStreamReadResponse, error) {
		return clinerpc.ReadStreamWithCaller(callHostCall, streamID)
	}
	callHostCall = clinerpc.Call
)

// SetHostCaller wires the host callback shared by every protocol adapter. It is
// called once from cliproxy_plugin_init before any RPC arrives.
func SetHostCaller(caller func(string, any) (json.RawMessage, error)) {
	if caller != nil {
		clinerpc.SetCaller(caller)
	}
}

// SetConfigFields records the plugin's declared configuration fields.
//
// The literal list lives in main.go rather than here on purpose: the repository
// tooling (scripts/release.go for the version literal, scripts/dev-sandbox.go's
// declaredConfigFields) globs plugins/<id>/*.go at the top level only, so a
// declaration buried in this subpackage would be invisible to both the release
// pipeline and the sandbox's "declared config fields are actually reported"
// assertion.
func SetConfigFields(fields []pluginapi.ConfigField) {
	configFieldsMu.Lock()
	configFields = append([]pluginapi.ConfigField(nil), fields...)
	configFieldsMu.Unlock()
}

func loadedConfigFields() []pluginapi.ConfigField {
	configFieldsMu.RLock()
	defer configFieldsMu.RUnlock()
	return append([]pluginapi.ConfigField(nil), configFields...)
}

func init() {
	configValue.Store(pluginConfig{
		ModelPrefix:       defaultModelPrefix,
		EnableModelPrefix: true,
	})
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		ModelPrefix:       defaultModelPrefix,
		EnableModelPrefix: true,
	}
}

// loadedConfig returns the effective plugin config with defaults applied.
func loadedConfig() pluginConfig {
	config, _ := configValue.Load().(pluginConfig)
	if strings.TrimSpace(config.ModelPrefix) == "" {
		config.ModelPrefix = defaultModelPrefix
	}
	if !strings.HasSuffix(config.ModelPrefix, "/") {
		config.ModelPrefix += "/"
	}
	return config
}

// modelPrefix returns the prefix applied to advertised model IDs, or "" when the
// operator turned the prefix off. The toggle exists so this plugin can coexist
// with another plugin (or a native CPA provider) that owns the bare Cline model
// IDs, per the repository's provider-prefix rule.
func modelPrefix() string {
	config := loadedConfig()
	if !config.EnableModelPrefix {
		return ""
	}
	return config.ModelPrefix
}

// stripModelPrefix removes this plugin's own prefix from a host-supplied model
// id before it is forwarded upstream.
//
// The host hands the executor the id as registered (cline/cline-pass/glm-5.3),
// but Cline's own API expects the bare id (cline-pass/glm-5.3). Only our own
// prefix is stripped, so a bare or foreign id passes through untouched.
func stripModelPrefix(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return model
	}
	prefix := modelPrefix()
	if prefix == "" {
		// The toggle is off, but the host may still hold prefixed ids from an
		// earlier registration; strip the configured default defensively.
		prefix = loadedConfig().ModelPrefix
	}
	if prefix != "" && strings.HasPrefix(model, prefix) {
		return strings.TrimPrefix(model, prefix)
	}
	return model
}

// parseStringList decodes a config value that may be a YAML flow list
// ([a, b]), a bare comma-separated list (a, b), or a single scalar. It is the
// same lenient convention the other plugins in this repository use, which keeps
// the plugin free of a YAML dependency for what are all flat scalar keys.
func parseStringList(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, "\"'")
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = raw[1 : len(raw)-1]
	}
	seen := map[string]struct{}{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		value := strings.Trim(strings.TrimSpace(part), "\"'")
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

// decodeConfigYAML accepts the config block as a plain string or as base64.
func decodeConfigYAML(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if decoded, errDecode := base64.StdEncoding.DecodeString(text); errDecode == nil && len(decoded) > 0 {
			return string(decoded)
		}
		return text
	}
	var blob []byte
	if err := json.Unmarshal(raw, &blob); err == nil {
		return string(blob)
	}
	return ""
}

// applyConfig decodes plugin config from the register/reconfigure request.
//
// Every key resets to its built-in default when absent, so removing a key from
// the host config reverts it. The host may resend Register/Reconfigure with a
// bare or foreign config block (for example during auth-store churn), which is
// why the decode is total rather than incremental.
func applyConfig(raw []byte) {
	if len(raw) == 0 {
		return
	}
	var req struct {
		ConfigYAML json.RawMessage `json:"config_yaml"`
	}
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return
	}
	config := defaultConfig()
	document := decodeConfigYAML(req.ConfigYAML)
	for _, line := range strings.Split(document, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "model_prefix:"):
			config.ModelPrefix = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "model_prefix:")), "\"'")
		case strings.HasPrefix(line, "enable_model_prefix:"):
			config.EnableModelPrefix = parseBool(strings.TrimPrefix(line, "enable_model_prefix:"), true)
		case strings.HasPrefix(line, "hidden_models:"):
			config.HiddenModels = parseStringList(strings.TrimPrefix(line, "hidden_models:"))
		case strings.HasPrefix(line, "models:"):
			config.Models = parseStringList(strings.TrimPrefix(line, "models:"))
		}
	}
	if strings.TrimSpace(config.ModelPrefix) == "" {
		config.ModelPrefix = defaultModelPrefix
	}
	if !strings.HasSuffix(config.ModelPrefix, "/") {
		config.ModelPrefix += "/"
	}
	configValue.Store(config)
	// The overlay is derived from the same config block, so both are applied
	// together and a reconfigure can never leave them disagreeing.
	configureModels(config.HiddenModels)
}

func parseBool(raw string, fallback bool) bool {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(raw), "\"'")) {
	case "true", "yes", "on", "1", "enabled":
		return true
	case "false", "no", "off", "0", "disabled":
		return false
	default:
		return fallback
	}
}

// registration answers plugin.register / plugin.reconfigure.
func registration(raw []byte) ([]byte, error) {
	applyConfig(raw)
	return okEnvelope(registrationPayload{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Cline",
			Version:          pluginVersion,
			Author:           "varcli",
			GitHubRepository: "https://github.com/varcli/cpa-plugins",
			Logo:             pluginLogoURL,
			ConfigFields:     loadedConfigFields(),
		},
		Capabilities: registrationCapability{
			ModelRegistrar:        false,
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{"chat-completions"},
			ExecutorOutputFormats: []string{"chat-completions"},
			ManagementAPI:         true,
		},
	})
}

// -----------------------------------------------------------------------------
// Small shared helpers
// -----------------------------------------------------------------------------

func okEnvelope(value any) ([]byte, error) {
	return clinerpc.OK(value)
}

func errorEnvelope(code, message string, retryable bool, status int) []byte {
	return clinerpc.Error(code, message, retryable, status)
}

func mustJSON(value any) []byte {
	return clinerpc.MustJSON(value)
}

func jsonHeaders() http.Header {
	return clinerpc.JSONHeaders()
}

func randomID() string {
	return clinerpc.RandomID()
}

func pluginHTTPStatus(err error) int {
	var statusError *upstreamStatusError
	if errors.As(err, &statusError) && statusError.status > 0 {
		return statusError.status
	}
	return http.StatusInternalServerError
}

func pluginErrorEnvelope(err error) []byte {
	if err == nil {
		return errorEnvelope("plugin_error", "unknown error", false, http.StatusInternalServerError)
	}
	return errorEnvelope("plugin_error", err.Error(), false, pluginHTTPStatus(err))
}

func emitPluginStream(streamID string, payload []byte) error {
	return clinerpc.EmitStreamWithCaller(callHostCall, streamID, payload)
}

func closePluginStream(streamID, errorMessage string) {
	clinerpc.ClosePluginStreamWithCaller(callHostCall, streamID, errorMessage)
}

func readAllHostHTTPStream(streamID string) ([]byte, error) {
	return clinerpc.ReadAllStreamWithCaller(callHostCall, streamID)
}

func closeHostHTTPStream(streamID string) {
	clinerpc.CloseStreamWithCaller(callHostCall, streamID)
}

func decodeHostAuthGetResponse(raw []byte) ([]byte, error) {
	// callHostCall 返回的已经是宿主 envelope 里的 result 本体
	// (callHost 在 main.go 中已剥掉外层), 这里直接解内层结构。
	var resp hostAuthGetResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("host.auth.get decode result: %w", err)
	}
	if len(resp.JSON) == 0 {
		return nil, fmt.Errorf("host.auth.get returned empty credential JSON")
	}
	return append([]byte(nil), resp.JSON...), nil
}
