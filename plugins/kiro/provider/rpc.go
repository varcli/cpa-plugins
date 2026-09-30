package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/varcli/cpa-plugins/plugins/kiro/kirorpc"
)

var configValue atomic.Value

var (
	hostHTTPDoCall = func(req hostHTTPRequest) (hostHTTPResponse, error) {
		return kirorpc.DoWithCaller(callHostCall, req)
	}
	hostHTTPDoStreamCall = func(req hostHTTPRequest) (hostHTTPStreamResponse, error) {
		return kirorpc.DoStreamWithCaller(callHostCall, req)
	}
	readHostHTTPStreamCall = func(streamID string) (hostHTTPStreamReadResponse, error) {
		return kirorpc.ReadStreamWithCaller(callHostCall, streamID)
	}
	callHostCall = kirorpc.Call
)

// SetHostCaller wires the host callback shared by every protocol adapter. It is
// called once from cliproxy_plugin_init before any RPC arrives.
func SetHostCaller(caller func(string, any) (json.RawMessage, error)) {
	if caller != nil {
		kirorpc.SetCaller(caller)
	}
}

func init() {
	configValue.Store(pluginConfig{ImportMode: "reference", LoginMode: defaultLoginMode, ModelPrefix: "kiro/"})
}

func loadedConfig() pluginConfig {
	config, _ := configValue.Load().(pluginConfig)
	if config.ImportMode == "" {
		config.ImportMode = "reference"
	}
	if config.ModelPrefix == "" {
		config.ModelPrefix = "kiro/"
	}
	config.LoginMode = normalizeLoginMode(config.LoginMode)
	if config.SSOStartURL == "" {
		config.SSOStartURL = defaultSSOStartURL
	}
	if config.BrowserSignInURL == "" {
		config.BrowserSignInURL = defaultSignInURL
	}
	if config.BrowserRedirectURI == "" {
		config.BrowserRedirectURI = defaultRedirectURI
	}
	if config.DesktopTokenURL == "" {
		config.DesktopTokenURL = defaultTokenURL
	}
	return config
}

func normalizeLoginMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "aws-device", "device", "device-code":
		return "aws-device"
	default:
		return defaultLoginMode
	}
}

// parseStringList decodes a config value that may be a YAML flow list
// ([a, b]), a bare comma-separated list (a, b), or a single scalar.
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

// applyConfig decodes plugin config from the register/reconfigure request.
//
// The host passes a plugin-scoped config_yaml document. Parsing is line-prefix
// based (the convention shared by the other plugins in this repository) rather
// than a YAML dependency: the plugin only owns flat scalar keys.
//
// login_mode is STICKY — it changes only when the incoming config explicitly
// carries a login_mode: line. The host may resend Register/Reconfigure with a
// bare or foreign config block (e.g. during auth-store churn), and resetting
// the login flow to the kiro-browser default mid-flight would strand users who
// configured aws-device. Every other key resets to its built-in default when
// absent, so removing a key reverts it.
func applyConfig(raw []byte) {
	if len(raw) == 0 {
		return
	}
	var req struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return
	}

	config := pluginConfig{
		ImportMode:         "reference",
		LoginMode:          loadedConfig().LoginMode, // sticky: keep unless overridden below
		ModelPrefix:        "kiro/",
		SSOStartURL:        defaultSSOStartURL,
		BrowserSignInURL:   defaultSignInURL,
		BrowserRedirectURI: defaultRedirectURI,
		DesktopTokenURL:    defaultTokenURL,
	}

	for _, line := range strings.Split(string(req.ConfigYAML), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "import_mode:"):
			config.ImportMode = normalizeMode(strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "import_mode:")), "\"'"))
		case strings.HasPrefix(line, "login_mode:"):
			config.LoginMode = normalizeLoginMode(strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "login_mode:")), "\"'"))
		case strings.HasPrefix(line, "api_region:"):
			config.APIRegion = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "api_region:")), "\"'")
		case strings.HasPrefix(line, "sso_region:"):
			config.SSORegion = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "sso_region:")), "\"'")
		case strings.HasPrefix(line, "sso_start_url:"):
			config.SSOStartURL = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "sso_start_url:")), "\"'")
		case strings.HasPrefix(line, "browser_redirect_uri:"):
			config.BrowserRedirectURI = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "browser_redirect_uri:")), "\"'")
		case strings.HasPrefix(line, "runtime_base_url:"):
			config.RuntimeBaseURL = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "runtime_base_url:")), "\"'")
		case strings.HasPrefix(line, "model_discovery_url:"):
			config.ModelDiscoveryURL = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "model_discovery_url:")), "\"'")
		case strings.HasPrefix(line, "usage_url:"):
			config.UsageURL = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "usage_url:")), "\"'")
		case strings.HasPrefix(line, "desktop_token_url:"):
			config.DesktopTokenURL = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "desktop_token_url:")), "\"'")
		case strings.HasPrefix(line, "desktop_refresh_url:"):
			config.DesktopRefreshURL = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "desktop_refresh_url:")), "\"'")
		case strings.HasPrefix(line, "oidc_refresh_url:"):
			config.OIDCRefreshURL = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "oidc_refresh_url:")), "\"'")
		case strings.HasPrefix(line, "browser_signin_url:"):
			config.BrowserSignInURL = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "browser_signin_url:")), "\"'")
		case strings.HasPrefix(line, "fingerprint:"):
			config.Fingerprint = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "fingerprint:")), "\"'")
		case strings.HasPrefix(line, "model_prefix:"):
			config.ModelPrefix = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "model_prefix:")), "\"'")
		case strings.HasPrefix(line, "static_models:"):
			if models := parseStringList(strings.TrimPrefix(line, "static_models:")); len(models) > 0 {
				config.StaticModels = models
			}
		}
	}

	if config.LoginMode == "" {
		config.LoginMode = defaultLoginMode
	}
	configValue.Store(config)
}

func registration(raw []byte) ([]byte, error) {
	applyConfig(raw)
	return okEnvelope(registrationPayload{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Kiro",
			Version:          "0.1.0",
			Author:           "varcli",
			GitHubRepository: "https://github.com/varcli/cpa-plugins",
			Logo:             pluginLogoURL,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "import_mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"reference", "copy"}, Description: "Default credential import ownership mode. reference follows the original kiro-cli/Amazon Q files; copy stores an independent snapshot."},
				{Name: "login_mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"kiro-browser", "aws-device"}, Description: "Flow used for NEW logins. aws-device supports Builder ID and IAM Identity Center (organization) accounts and is recommended for remote CPA servers."},
				{Name: "api_region", Type: pluginapi.ConfigFieldTypeString, Description: "Kiro runtime region, usually us-east-1; independent of the AWS SSO region."},
				{Name: "sso_region", Type: pluginapi.ConfigFieldTypeString, Description: "Fallback AWS SSO OIDC region."},
				{Name: "sso_start_url", Type: pluginapi.ConfigFieldTypeString, Description: "Determines the aws-device account type: https://view.awsapps.com/start for Builder ID, or the organization's AWS access portal URL for IAM Identity Center."},
				{Name: "browser_redirect_uri", Type: pluginapi.ConfigFieldTypeString, Description: "Used only by browser login modes. Production Kiro requires localhost (default http://localhost:3128) or an app.kiro.dev subdomain."},
				{Name: "runtime_base_url", Type: pluginapi.ConfigFieldTypeString, Description: "Optional Kiro runtime base URL override for private gateways and tests."},
				{Name: "model_discovery_url", Type: pluginapi.ConfigFieldTypeString, Description: "Optional Kiro ListAvailableModels service endpoint override. Defaults to https://q.{region}.amazonaws.com/."},
				{Name: "usage_url", Type: pluginapi.ConfigFieldTypeString, Description: "Optional Kiro GetUsageLimits service endpoint override. Defaults to https://q.{region}.amazonaws.com/."},
				{Name: "static_models", Type: pluginapi.ConfigFieldTypeArray, Description: "Additional Kiro runtime model IDs advertised when live discovery is unavailable."},
			},
		},
		Capabilities: registrationCapability{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{"chat-completions"},
			ExecutorOutputFormats: []string{"chat-completions"},
			CommandLinePlugin:     true,
			ManagementAPI:         true,
		},
	})
}

func executeCommandLine(raw []byte) ([]byte, error) {
	var req commandLineExecutionRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	pathFlag := req.Flags["kiro-import"]
	if triggered, exists := req.TriggeredFlags["kiro-import"]; exists {
		pathFlag = triggered
	}
	path := strings.TrimSpace(pathFlag.Value)
	if path == "" {
		return okEnvelope(commandLineExecutionResponse{Stderr: []byte("--kiro-import requires a credential file or directory\n"), ExitCode: 2})
	}
	mode := loadedConfig().ImportMode
	if modeFlag, exists := req.Flags["kiro-import-mode"]; exists && strings.TrimSpace(modeFlag.Value) != "" {
		mode = modeFlag.Value
	}
	mode = normalizeMode(mode)
	creds, errImport := importCredentials(path, mode)
	if errImport != nil {
		return okEnvelope(commandLineExecutionResponse{Stderr: []byte(errImport.Error() + "\n"), ExitCode: 1})
	}
	auths := make([]authData, 0, len(creds))
	for _, cred := range creds {
		auth, errAuth := authDataFromCredential(cred)
		if errAuth != nil {
			return nil, errAuth
		}
		// Let the host derive the file-based record ID from FileName so the
		// imported file, its manager record, and later auth.parse scans agree.
		auth.ID = ""
		auths = append(auths, auth)
	}
	message := fmt.Sprintf("Imported %d Kiro account(s) in %s mode.\n", len(auths), mode)
	return okEnvelope(commandLineExecutionResponse{Stdout: []byte(message), Auths: auths, ExitCode: 0})
}

func readAllHostHTTPStream(streamID string) ([]byte, error) {
	return kirorpc.ReadAllStreamWithCaller(callHostCall, streamID)
}

func closeHostHTTPStream(streamID string) {
	kirorpc.CloseStreamWithCaller(callHostCall, streamID)
}

func emitPluginStream(streamID string, payload []byte) error {
	return kirorpc.EmitStreamWithCaller(callHostCall, streamID, payload)
}

func closePluginStream(streamID, errorMessage string) {
	kirorpc.ClosePluginStreamWithCaller(callHostCall, streamID, errorMessage)
}

func okEnvelope(value any) ([]byte, error) {
	return kirorpc.OK(value)
}

func errorEnvelope(code, message string, retryable bool, status int) []byte {
	return kirorpc.Error(code, message, retryable, status)
}

func mustJSON(value any) []byte {
	return kirorpc.MustJSON(value)
}

func jsonHeaders() http.Header {
	return kirorpc.JSONHeaders()
}

func managementJSON(status int, body map[string]any) []byte {
	return kirorpc.ManagementJSON(status, body)
}

func randomID() string {
	return kirorpc.RandomID()
}

func pluginHTTPStatus(err error) int {
	if typed, ok := err.(statusError); ok {
		return typed.HTTPStatus
	}
	return http.StatusInternalServerError
}
