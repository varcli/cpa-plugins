// Package clinerpc carries the host RPC plumbing shared by every cline
// protocol adapter: the host callback handle, the request/response envelope,
// the host HTTP bridge (host.http.do / do_stream / stream_read / stream_close),
// plugin stream emission, and the small JSON helpers the management layer
// reuses.
//
// It is deliberately protocol-only: nothing in this package knows what a Cline
// credential is. That keeps the C ABI layer (main.go) and the provider
// (provider/) free to share one definition of the wire contract.
package clinerpc

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Caller is the host callback exposed by the plugin ABI.
type Caller func(string, any) (json.RawMessage, error)

var caller Caller = func(string, any) (json.RawMessage, error) {
	return nil, errors.New("host callback is unavailable")
}

// SetCaller wires the host callback shared by all protocol adapters.
func SetCaller(value Caller) {
	if value != nil {
		caller = value
	}
}

// Call invokes one host RPC method through the wired callback.
func Call(method string, payload any) (json.RawMessage, error) {
	return caller(method, payload)
}

// HTTPRequest is one upstream call routed through the host bridge.
type HTTPRequest struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	Method         string      `json:"method"`
	URL            string      `json:"url"`
	Headers        http.Header `json:"headers,omitempty"`
	Body           []byte      `json:"body,omitempty"`
}

// HTTPResponse is the fully buffered reply of a bridged call.
type HTTPResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

// HTTPStreamResponse is the handle of an in-flight bridged stream.
type HTTPStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
	StreamID   string      `json:"stream_id"`
}

// HTTPStreamReadResponse is one chunk pulled from a bridged stream.
type HTTPStreamReadResponse struct {
	Payload []byte `json:"payload"`
	Error   string `json:"error"`
	Done    bool   `json:"done"`
}

// Do performs a non-streaming upstream call through the host bridge.
func Do(req HTTPRequest) (HTTPResponse, error) {
	return DoWithCaller(caller, req)
}

// DoWithCaller is Do against an explicit callback, used by the provider so a
// single wiring point (SetHostCaller) controls every adapter.
func DoWithCaller(call Caller, req HTTPRequest) (HTTPResponse, error) {
	result, err := call("host.http.do", req)
	if err != nil {
		return HTTPResponse{}, err
	}
	var response HTTPResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return HTTPResponse{}, err
	}
	return response, nil
}

// DoStream opens a streaming upstream call through the host bridge.
func DoStream(req HTTPRequest) (HTTPStreamResponse, error) {
	return DoStreamWithCaller(caller, req)
}

// DoStreamWithCaller is DoStream against an explicit callback.
func DoStreamWithCaller(call Caller, req HTTPRequest) (HTTPStreamResponse, error) {
	result, err := call("host.http.do_stream", req)
	if err != nil {
		return HTTPStreamResponse{}, err
	}
	var response HTTPStreamResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return HTTPStreamResponse{}, err
	}
	return response, nil
}

// ReadStream pulls one chunk from a bridged stream.
func ReadStream(streamID string) (HTTPStreamReadResponse, error) {
	return ReadStreamWithCaller(caller, streamID)
}

// ReadStreamWithCaller is ReadStream against an explicit callback.
func ReadStreamWithCaller(call Caller, streamID string) (HTTPStreamReadResponse, error) {
	result, err := call("host.http.stream_read", map[string]string{"stream_id": streamID})
	if err != nil {
		return HTTPStreamReadResponse{}, err
	}
	var response HTTPStreamReadResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return HTTPStreamReadResponse{}, err
	}
	return response, nil
}

// ReadAllStream drains a bridged stream into memory.
func ReadAllStream(streamID string) ([]byte, error) {
	return ReadAllStreamWithCaller(caller, streamID)
}

// ReadAllStreamWithCaller is ReadAllStream against an explicit callback.
func ReadAllStreamWithCaller(call Caller, streamID string) ([]byte, error) {
	var body []byte
	for {
		chunk, err := ReadStreamWithCaller(call, streamID)
		if err != nil {
			return body, err
		}
		body = append(body, chunk.Payload...)
		if chunk.Error != "" {
			return body, errors.New(chunk.Error)
		}
		if chunk.Done {
			return body, nil
		}
	}
}

// CloseStream aborts a bridged upstream stream.
func CloseStream(streamID string) {
	CloseStreamWithCaller(caller, streamID)
}

// CloseStreamWithCaller is CloseStream against an explicit callback.
func CloseStreamWithCaller(call Caller, streamID string) {
	if streamID == "" {
		return
	}
	_, _ = call("host.http.stream_close", map[string]string{"stream_id": streamID})
}

// EmitStream pushes one chunk to a plugin-owned client stream.
func EmitStream(streamID string, payload []byte) error {
	return EmitStreamWithCaller(caller, streamID, payload)
}

// EmitStreamWithCaller is EmitStream against an explicit callback.
func EmitStreamWithCaller(call Caller, streamID string, payload []byte) error {
	_, err := call("host.stream.emit", map[string]any{"stream_id": streamID, "payload": payload})
	return err
}

// ClosePluginStream ends a plugin-owned client stream, carrying an optional
// error message for the client.
func ClosePluginStream(streamID, errorMessage string) {
	ClosePluginStreamWithCaller(caller, streamID, errorMessage)
}

// ClosePluginStreamWithCaller is ClosePluginStream against an explicit callback.
func ClosePluginStreamWithCaller(call Caller, streamID, errorMessage string) {
	_, _ = call("host.stream.close", map[string]any{"stream_id": streamID, "error": errorMessage})
}

// Envelope is the plugin ABI result wrapper shared by every RPC reply.
type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *EnvelopeError  `json:"error,omitempty"`
}

// EnvelopeError carries the failure code, message and the HTTP status the host
// uses for per-status credential cooldown policy.
type EnvelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

// OK wraps a successful result in the plugin envelope.
func OK(value any) ([]byte, error) {
	result, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{OK: true, Result: result})
}

// Error builds a failed envelope.
func Error(code, message string, retryable bool, status int) []byte {
	raw, _ := json.Marshal(Envelope{OK: false, Error: &EnvelopeError{
		Code: code, Message: message, Retryable: retryable, HTTPStatus: status,
	}})
	return raw
}

// MustJSON marshals a value the wire structs guarantee is marshalable.
func MustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

// JSONHeaders is the response header set every management JSON reply uses.
func JSONHeaders() http.Header {
	return http.Header{
		"Content-Type":  []string{"application/json; charset=utf-8"},
		"Cache-Control": []string{"no-store"},
	}
}

// RandomID returns a random UUIDv4-shaped identifier. It is only used for
// correlation ids (request ids, tool-call ids) where uniqueness matters and
// cryptographic strength does not.
func RandomID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return fmt.Sprintf("%p", &data)
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	value := hex.EncodeToString(data)
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:]
}
