package provider

// executor.go turns an OpenAI-compatible chat request into a Cline
// chat/completions call and back.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// upstreamStatusError carries the HTTP status an upstream failure should surface
// as, so the host's per-status cooldown policy sees the real code rather than a
// blanket 500.
type upstreamStatusError struct {
	status  int
	message string
}

func (e *upstreamStatusError) Error() string { return e.message }

// clineUpstreamError renders an upstream error body as a typed status error.
func clineUpstreamError(status int, body []byte) error {
	message := strings.TrimSpace(string(body))
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		if parsed.Error.Code != "" {
			message = parsed.Error.Code + ": " + parsed.Error.Message
		} else {
			message = parsed.Error.Message
		}
	}
	if status == http.StatusForbidden && strings.Contains(message, "ENTITLEMENT_ERROR") {
		return &upstreamStatusError{
			status:  status,
			message: "ClinePass subscription is not active for this account; subscribe or use a free model",
		}
	}
	// Cline answers 500 `empty response content` when the request itself leaves
	// no room for a reply — observed with reasoning models where max_tokens is
	// consumed by the reasoning phase, so the visible content comes back empty.
	// That is a client-side parameter problem, not an upstream outage, and CPA
	// cools credentials down on 5xx. Report 400 so one bad request cannot take
	// the whole account out of rotation.
	if isEmptyContentError(message) {
		return &upstreamStatusError{
			status:  http.StatusBadRequest,
			message: "upstream produced no content: " + truncate(message, 200) + " (try a larger max_tokens)",
		}
	}
	return &upstreamStatusError{
		status:  status,
		message: fmt.Sprintf("upstream %d: %s", status, truncate(message, 240)),
	}
}

// isEmptyContentError matches Cline's "the request produced nothing" body.
func isEmptyContentError(message string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(message)), "empty response content")
}

// asUpstreamStatusError unwraps the typed upstream errors the executor raises so
// the host sees the right status instead of a generic failure.
func asUpstreamStatusError(err error) (*upstreamStatusError, bool) {
	var statusError *upstreamStatusError
	if errors.As(err, &statusError) {
		return statusError, true
	}
	return nil, false
}

// normalizeUpstreamResponse flattens Cline's non-streaming envelope. The
// chat/completions endpoint returns {"data":{"choices":...}} while
// OpenAI-compatible CPA clients expect choices at the top level. Streaming
// responses are already emitted as standard chat.completion.chunk frames.
func normalizeUpstreamResponse(body []byte) []byte {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Data) == 0 {
		return body
	}
	var direct struct {
		Choices json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(body, &direct); err == nil && len(direct.Choices) > 0 {
		return body
	}
	var nested struct {
		Choices json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(envelope.Data, &nested); err != nil || len(nested.Choices) == 0 {
		return body
	}
	return envelope.Data
}

// requestPayload picks the body CPA handed us, preferring the rewritten payload
// over the original request.
func requestPayload(req *pluginapi.ExecutorRequest) []byte {
	if len(req.Payload) > 0 {
		return req.Payload
	}
	return req.OriginalRequest
}

// handleExecExecute answers executor.execute: one buffered chat completion.
func handleExecExecute(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	payload := requestPayload(&req)
	cred, err := prepareCredential(sa, req.AuthAttributes)
	if err != nil {
		return nil, err
	}
	response, err := cred.doBuffered(func(current *storedAuth) (bufferedResponse, error) {
		body := buildUpstreamPayload(payload, stripModelPrefix(req.Model), false)
		return sendUpstreamChat(body, current.Auth.AccessToken, "application/json")
	})
	if err != nil {
		if statusError, ok := asUpstreamStatusError(err); ok {
			return errorEnvelope("http_error", statusError.message, false, statusError.status), nil
		}
		return nil, err
	}
	if response.StatusCode >= 400 {
		upstreamErr := clineUpstreamError(response.StatusCode, response.Body)
		return errorEnvelope("http_error", upstreamErr.Error(), false, pluginHTTPStatus(upstreamErr)), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: normalizeUpstreamResponse(response.Body)})
}

// handleExecStream answers executor.execute_stream. A non-empty StreamID means
// the host wants chunks pushed through host.stream.emit; otherwise they are
// returned inline.
func handleExecStream(raw []byte) ([]byte, error) {
	var req executorStreamRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	payload := requestPayload(&req.ExecutorRequest)
	headers := streamHeaders()
	sseFramed := clientNeedsSSEFrame(req.Metadata)
	cred, err := prepareCredential(sa, req.AuthAttributes)
	if err != nil {
		return nil, err
	}
	if req.StreamID == "" {
		chunks, status, errCollect := collectUpstreamStream(payload, cred, req.Model, sseFramed)
		if errCollect != nil {
			if statusError, ok := asUpstreamStatusError(errCollect); ok {
				return errorEnvelope("http_error", statusError.message, false, statusError.status), nil
			}
			if status <= 0 {
				status = http.StatusBadGateway
			}
			return errorEnvelope("http_error", errCollect.Error(), false, status), nil
		}
		return okEnvelope(streamResponse{Headers: headers, Chunks: chunks})
	}
	go pumpUpstreamStream(payload, cred, req.StreamID, sseFramed, req.Model, time.Now())
	return okEnvelope(streamResponse{Headers: headers})
}

// countTokens answers executor.count_tokens. Cline exposes no tokenizer
// endpoint, so the plugin reports a character-based estimate rather than
// inventing a number the host would trust as exact.
func countTokens(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	payload := requestPayload(&req)
	var parsed struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	characters := 0
	if json.Unmarshal(payload, &parsed) == nil {
		for _, message := range parsed.Messages {
			characters += textLength(message.Content)
		}
	}
	if characters == 0 {
		characters = len(payload)
	}
	// Four characters per token is the standard rough ratio for English and code.
	body := mustJSON(map[string]any{"total_tokens": characters/4 + 1})
	return okEnvelope(pluginapi.ExecutorResponse{Payload: body, Headers: jsonHeaders()})
}

// textLength measures a message content field, which may be a string or the
// multimodal array form.
func textLength(content any) int {
	switch value := content.(type) {
	case string:
		return len(value)
	case []any:
		total := 0
		for _, part := range value {
			if object, ok := part.(map[string]any); ok {
				if text, ok := object["text"].(string); ok {
					total += len(text)
				}
			}
		}
		return total
	default:
		return 0
	}
}
