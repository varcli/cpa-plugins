package provider

// export.go is the plugin's public surface: one exported entry point per RPC
// method the host dispatches (see main.go's handleMethod). The protocol handlers
// stay unexported so the package keeps a single, explicit boundary between the C
// ABI layer and the provider internals.

import (
	"encoding/json"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Register handles plugin.register / plugin.reconfigure: it decodes the plugin
// config and returns the registration payload.
func Register(raw []byte) ([]byte, error) { return registration(raw) }

// Identifier answers auth.identifier / executor.identifier.
func Identifier() ([]byte, error) {
	return okEnvelope(map[string]string{"identifier": providerID})
}

// StaticModels answers model.static. The request is only used to refresh config;
// the catalog itself does not depend on login state.
func StaticModels(raw []byte) ([]byte, error) { return modelsStatic(raw) }

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
func Execute(raw []byte) ([]byte, error) { return handleExecExecute(raw) }

// ExecuteStream answers executor.execute_stream.
func ExecuteStream(raw []byte) ([]byte, error) { return handleExecStream(raw) }

// CountTokens answers executor.count_tokens.
func CountTokens(raw []byte) ([]byte, error) { return countTokens(raw) }

// ExecutorHTTPRequest answers executor.http_request.
func ExecutorHTTPRequest(raw []byte) ([]byte, error) { return executorHTTPRequest(raw) }

// RegisterManagement answers management.register.
func RegisterManagement() ([]byte, error) { return okEnvelope(managementRegistration()) }

// HandleManagement answers management.handle.
func HandleManagement(raw []byte) ([]byte, error) { return handleManagement(raw) }

// executorHTTPRequest performs a plugin-supplied upstream call, applying Cline's
// account headers when the caller did not set an Authorization header of its own.
//
// This is a plugin-supplied call rather than a proxy: the host supplies the URL,
// and the plugin only fills in the credential the operator configured. Without
// the header merge a bridged call would go out unauthenticated and 401.
func executorHTTPRequest(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorHTTPRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	headers := req.Headers
	if headers == nil {
		headers = http.Header{}
	}
	if sa, err := parseStored(req.StorageJSON); err == nil && len(headers.Values("Authorization")) == 0 {
		for key, values := range clineHeaders(sa.Auth.AccessToken) {
			if len(headers.Values(key)) == 0 {
				headers[key] = values
			}
		}
	}
	response, err := hostHTTPDoCall(hostHTTPRequest{
		Method:  req.Method,
		URL:     req.URL,
		Headers: headers,
		Body:    req.Body,
	})
	if err != nil {
		return pluginErrorEnvelope(err), nil
	}
	return okEnvelope(pluginapi.ExecutorHTTPResponse{
		StatusCode: response.StatusCode,
		Headers:    response.Headers,
		Body:       response.Body,
	})
}
