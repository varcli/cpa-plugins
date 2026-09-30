package provider

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/varcli/cpa-plugins/plugins/cline/clinenx"
)

// http.go is the single outbound-call path for the plugin.
//
// Every upstream request — WorkOS device grant, Cline token register/refresh,
// recommended-models discovery, users/me, chat completions — goes through the
// host HTTP bridge (host.http.do / host.http.do_stream) so CPA's request log
// captures it and host transport policy (proxy, timeouts, TLS) applies. There is
// deliberately no direct http.Client fallback: a plugin that quietly bypasses
// the bridge produces a deployment whose outbound traffic is invisible in the
// host's logs, which is exactly the failure the bridge exists to prevent.

// clineHeaders builds the header set Cline's API expects for an account.
func clineHeaders(accessToken string) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+ensureWorkOSPrefix(accessToken))
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json")
	headers.Set("HTTP-Referer", clineAppBase)
	headers.Set("X-Title", "Cline")
	headers.Set("X-IS-MULTIROOT", "false")
	headers.Set("X-CLIENT-TYPE", "cline-sdk")
	headers.Set("X-CLIENT-VERSION", clineVersion)
	headers.Set("X-PLATFORM", "linux")
	headers.Set("X-PLATFORM-VERSION", clineVersion)
	headers.Set("X-CORE-VERSION", clineVersion)
	headers.Set("User-Agent", clineUserAgent)
	return headers
}

// ensureWorkOSPrefix marks a raw WorkOS access token the way Cline's API expects
// ("Bearer workos:<token>").
func ensureWorkOSPrefix(token string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), workOSTokenPfx) {
		return token
	}
	return workOSTokenPfx + token
}

func stripWorkOSPrefix(token string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), workOSTokenPfx) {
		return token[len(workOSTokenPfx):]
	}
	return token
}

// bufferedResponse is a fully-read upstream reply. Every caller of the
// non-streaming path wants the whole body, so reading it here keeps the bridge
// usage in one place.
type bufferedResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// clineRequest performs one buffered upstream call through the host bridge.
func clineRequest(method, target, contentType string, body io.Reader, headers http.Header) (bufferedResponse, error) {
	var payload []byte
	if body != nil {
		raw, errRead := io.ReadAll(body)
		if errRead != nil {
			return bufferedResponse{}, errRead
		}
		payload = raw
	}
	final := http.Header{}
	if contentType != "" {
		final.Set("Content-Type", contentType)
	}
	for key, values := range headers {
		for _, value := range values {
			final.Add(key, value)
		}
	}
	response, err := hostHTTPDoCall(hostHTTPRequest{
		Method:  method,
		URL:     target,
		Headers: final,
		Body:    payload,
	})
	if err != nil {
		return bufferedResponse{}, err
	}
	return bufferedResponse{StatusCode: response.StatusCode, Headers: response.Headers, Body: response.Body}, nil
}

// postJSON marshals payload and POSTs it with the given extra headers.
func postJSON(target string, payload any, headers http.Header) (bufferedResponse, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return bufferedResponse{}, err
	}
	return clineRequest(http.MethodPost, target, "application/json", bytes.NewReader(raw), headers)
}

// clineGet performs an authenticated GET against Cline's API.
func clineGet(target string, headers http.Header) (bufferedResponse, error) {
	return clineRequest(http.MethodGet, target, "", nil, headers)
}

// openUpstreamStream starts a streaming chat completion through the host bridge.
// The caller owns the returned stream id and must close it.
func openUpstreamStream(payload []byte, accessToken string) (hostHTTPStreamResponse, error) {
	headers := clineHeaders(accessToken)
	headers.Set("Accept", "text/event-stream")
	return hostHTTPDoStreamCall(hostHTTPRequest{
		Method:  http.MethodPost,
		URL:     clineAPIBase + "/api/v1/chat/completions",
		Headers: headers,
		Body:    payload,
	})
}

// sendUpstreamChat performs one buffered chat completion.
func sendUpstreamChat(payload []byte, accessToken string, accept string) (bufferedResponse, error) {
	headers := clineHeaders(accessToken)
	if accept != "" {
		headers.Set("Accept", accept)
	}
	return clineRequest(http.MethodPost, clineAPIBase+"/api/v1/chat/completions", "application/json", bytes.NewReader(payload), headers)
}

// rewriteStreamFlag forces the stream field on a chat payload. Cline defaults to
// streaming, so the non-streaming path must set it explicitly and the streaming
// path must not inherit a stale false.
func rewriteStreamFlag(payload []byte, stream bool) []byte {
	var parsed map[string]any
	if json.Unmarshal(payload, &parsed) != nil {
		return payload
	}
	parsed["stream"] = stream
	rewritten, err := json.Marshal(parsed)
	if err != nil {
		return payload
	}
	return rewritten
}

// setPayloadModel pins the upstream model id on a chat payload.
func setPayloadModel(payload []byte, model string) []byte {
	if strings.TrimSpace(model) == "" {
		return payload
	}
	var parsed map[string]any
	if json.Unmarshal(payload, &parsed) != nil {
		return payload
	}
	parsed["model"] = model
	rewritten, err := json.Marshal(parsed)
	if err != nil {
		return payload
	}
	return rewritten
}

// buildUpstreamPayload applies the model rewrite and the stream flag in one
// marshal so a request body is never rewritten twice.
func buildUpstreamPayload(payload []byte, model string, stream bool) []byte {
	var parsed map[string]any
	if json.Unmarshal(payload, &parsed) != nil {
		return payload
	}
	if strings.TrimSpace(model) != "" {
		parsed["model"] = model
	}
	parsed["stream"] = stream
	rewritten, err := json.Marshal(parsed)
	if err != nil {
		return payload
	}
	return rewritten
}

func truncate(value string, max int) string { return clinenx.Truncate(value, max) }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
