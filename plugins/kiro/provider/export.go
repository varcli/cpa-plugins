package provider

import (
	"strings"
	"sync"
)

// export.go is the plugin's public surface: one exported entry point per RPC
// method the host dispatches (see main.go's handleMethod). The protocol
// handlers below stay unexported so the package keeps a single, explicit
// boundary between the C ABI layer and the provider internals.

// Register handles plugin.register / plugin.reconfigure: it decodes the
// plugin config and returns the registration payload.
func Register(raw []byte) ([]byte, error) { return registration(raw) }

// Identifier answers auth.identifier / executor.identifier.
func Identifier() ([]byte, error) {
	return okEnvelope(map[string]string{"identifier": providerID})
}

// StaticModels answers model.static. The request is only used to refresh
// config; the catalog itself does not depend on login state.
func StaticModels(raw []byte) ([]byte, error) {
	applyConfig(raw)
	return okEnvelope(modelResponse{Provider: providerID, Models: staticModels()})
}

// ModelsForAuth answers model.for_auth.
func ModelsForAuth(raw []byte) ([]byte, error) { return modelsForAuth(raw) }

// ParseAuth answers auth.parse.
func ParseAuth(raw []byte) ([]byte, error) { return parseAuth(raw) }

// StartLogin answers auth.login.start.
func StartLogin(raw []byte) ([]byte, error) { return startLogin(raw) }

// PollLogin answers auth.login.poll.
func PollLogin(raw []byte) ([]byte, error) { return pollLogin(raw) }

// RefreshAuth answers auth.refresh.
func RefreshAuth(raw []byte) ([]byte, error) { return refreshAuth(raw) }

// Execute answers executor.execute (non-streaming chat completion).
func Execute(raw []byte) ([]byte, error) { return executeRequest(raw) }

// ExecuteStream answers executor.execute_stream.
func ExecuteStream(raw []byte) ([]byte, error) { return executeStream(raw) }

// CountTokens answers executor.count_tokens.
func CountTokens(raw []byte) ([]byte, error) { return countTokens(raw) }

// ExecutorHTTPRequest answers executor.http_request.
func ExecutorHTTPRequest(raw []byte) ([]byte, error) { return executorHTTPRequest(raw) }

// RegisterCommandLine answers command_line.register with the --kiro-import
// flag definitions.
func RegisterCommandLine() ([]byte, error) {
	return okEnvelope(map[string]any{"Flags": []any{
		map[string]any{"Name": "kiro-import", "Usage": "Import Kiro IDE, kiro-cli, Amazon Q, or AWS SSO credentials", "Type": "string"},
		map[string]any{"Name": "kiro-import-mode", "Usage": "Kiro credential ownership mode: reference or copy", "Type": "string", "DefaultValue": "reference"},
	}})
}

// ExecuteCommandLine answers command_line.execute.
func ExecuteCommandLine(raw []byte) ([]byte, error) { return executeCommandLine(raw) }

// managementBasePath holds the host-injected management BasePath so
// HandleManagement does not hardcode /v0/management (tolerating future host
// path changes).
var (
	managementBasePathCache   = "/v0/management"
	managementBasePathCacheMu sync.RWMutex
)

// SetManagementBasePath records the BasePath reported by management.register.
func SetManagementBasePath(path string) {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	if path == "" {
		return
	}
	managementBasePathCacheMu.Lock()
	managementBasePathCache = path
	managementBasePathCacheMu.Unlock()
}

func loadedManagementBasePath() string {
	managementBasePathCacheMu.RLock()
	defer managementBasePathCacheMu.RUnlock()
	return managementBasePathCache
}

// RegisterManagement answers management.register.
func RegisterManagement() ([]byte, error) { return registerManagement() }

// HandleManagement answers management.handle.
func HandleManagement(raw []byte) ([]byte, error) { return handleManagement(raw) }
