package provider

// stream.go drives the two streaming paths the host offers: pushing chunks to a
// client-owned stream (host.stream.emit) and collecting a stream inline when the
// host asked for chunks in the reply body.
//
// Both read the upstream through a bridged stream id rather than an *http.Response,
// because the host owns the socket. A scanner over the bridge reader keeps the
// SSE parsing identical to the reference implementation.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func emptyStreamError() error {
	return fmt.Errorf("empty upstream stream (unexpected EOF)")
}

func upstreamReadError(err error) error {
	return fmt.Errorf("upstream stream read error (unexpected EOF): %w", err)
}

func streamEmit(streamID string, payload []byte) error {
	if streamID == "" {
		return fmt.Errorf("no stream id")
	}
	return emitPluginStream(streamID, payload)
}

// streamEmitError pushes one error frame to the client. The host closes the
// stream afterwards, so a failed emit here is not worth reporting.
func streamEmitError(streamID, message string) {
	if streamID == "" {
		return
	}
	_ = emitPluginStream(streamID, mustJSON(map[string]any{
		"stream_id": streamID,
		"error":     truncate(message, 600),
	}))
}

var streamCloseOnce sync.Map

// streamClose ends a plugin-owned stream exactly once, even when the pump and a
// deferred cleanup both reach it.
func streamClose(streamID string) {
	if streamID == "" {
		return
	}
	actual, _ := streamCloseOnce.LoadOrStore(streamID, &sync.Once{})
	actual.(*sync.Once).Do(func() {
		closePluginStream(streamID, "")
		streamCloseOnce.Delete(streamID)
	})
}

func streamHeaders() http.Header {
	headers := http.Header{}
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("X-Accel-Buffering", "no")
	return headers
}

// clientNeedsSSEFrame reports whether the client expects raw SSE framing. CPA's
// own /v1/chat/completions endpoint re-frames chunks itself, so sending "data: "
// prefixes there would double-wrap every event.
func clientNeedsSSEFrame(metadata map[string]any) bool {
	path, _ := metadata["request_path"].(string)
	switch strings.ToLower(strings.TrimSpace(path)) {
	case "/v1/chat/completions", "/v1/completions":
		return false
	default:
		return true
	}
}

// hostStreamReader adapts a bridged upstream stream to io.Reader so the SSE
// parser can be an ordinary bufio.Scanner.
type hostStreamReader struct {
	streamID string
	pending  []byte
	done     bool
	err      error
}

func (r *hostStreamReader) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.done {
			return 0, io.EOF
		}
		chunk, err := readHostHTTPStreamCall(r.streamID)
		if err != nil {
			r.err = err
			return 0, err
		}
		r.pending = chunk.Payload
		if chunk.Error != "" {
			r.err = fmt.Errorf("%s", chunk.Error)
		}
		if chunk.Done {
			r.done = true
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// pumpUpstreamStream streams one chat completion to the client, refreshing the
// credential first so a token that expired since the host last persisted it
// still serves the request. Nothing has been emitted when a 401 arrives, which
// is what makes the retry inside openStream safe here.
func pumpUpstreamStream(payload []byte, cred *credential, streamID string, sseFramed bool, requestedModel string, started time.Time) {
	defer streamClose(streamID)
	stream, err := cred.openStream(func(current *storedAuth) (hostHTTPStreamResponse, error) {
		return openUpstreamStream(buildUpstreamPayload(payload, stripModelPrefix(requestedModel), true), current.Auth.AccessToken)
	})
	if err != nil {
		streamEmitError(streamID, err.Error())
		return
	}
	defer closeHostHTTPStream(stream.StreamID)
	if stream.StatusCode >= 400 {
		body, _ := readAllHostHTTPStream(stream.StreamID)
		streamEmitError(streamID, clineUpstreamError(stream.StatusCode, body).Error())
		return
	}
	emitted, err := pumpSSE(&hostStreamReader{streamID: stream.StreamID}, streamID, sseFramed)
	if err != nil {
		streamEmitError(streamID, err.Error())
		return
	}
	if !emitted {
		streamEmitError(streamID, emptyStreamError().Error())
	}
	_ = started
}

// pumpSSE parses an SSE body and pushes each JSON event to the client stream.
// It reports whether anything was emitted so an empty 200 can be reported as an
// error rather than silently closing.
func pumpSSE(body io.Reader, streamID string, sseFramed bool) (bool, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	emitted := false
	for scanner.Scan() {
		data, ok := sseData(scanner.Text())
		if !ok {
			continue
		}
		if err := streamEmit(streamID, sseChunkPayload(data, sseFramed)); err != nil {
			return emitted, nil
		}
		emitted = true
	}
	if err := scanner.Err(); err != nil {
		return emitted, upstreamReadError(err)
	}
	return emitted, nil
}

// collectUpstreamStream drains a streaming reply into chunks for the host's
// inline-stream form.
func collectUpstreamStream(payload []byte, cred *credential, model string, sseFramed bool) ([]pluginapi.ExecutorStreamChunk, int, error) {
	stream, err := cred.openStream(func(current *storedAuth) (hostHTTPStreamResponse, error) {
		return openUpstreamStream(buildUpstreamPayload(payload, stripModelPrefix(model), true), current.Auth.AccessToken)
	})
	if err != nil {
		if statusError, ok := asUpstreamStatusError(err); ok {
			return nil, statusError.status, statusError
		}
		return nil, 0, err
	}
	defer closeHostHTTPStream(stream.StreamID)
	if stream.StatusCode >= 400 {
		body, _ := readAllHostHTTPStream(stream.StreamID)
		return nil, stream.StatusCode, clineUpstreamError(stream.StatusCode, body)
	}
	scanner := bufio.NewScanner(&hostStreamReader{streamID: stream.StreamID})
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	chunks := make([]pluginapi.ExecutorStreamChunk, 0, 64)
	emitted := false
	for scanner.Scan() {
		data, ok := sseData(scanner.Text())
		if !ok {
			continue
		}
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: sseChunkPayload(data, sseFramed)})
		emitted = true
	}
	if err := scanner.Err(); err != nil {
		return chunks, stream.StatusCode, upstreamReadError(err)
	}
	if !emitted {
		return chunks, stream.StatusCode, emptyStreamError()
	}
	return chunks, stream.StatusCode, nil
}

// sseData extracts the JSON payload of one SSE line. Blank lines, comments and
// non-data fields are skipped, and the terminator reports ok=false so callers
// stop without treating it as a chunk.
func sseData(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "data:") {
		return "", false
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" || !json.Valid([]byte(data)) {
		return "", false
	}
	return data, true
}

// sseChunkPayload renders one event for the client, adding SSE framing only when
// the client reads the stream raw.
func sseChunkPayload(data string, sseFramed bool) []byte {
	if sseFramed {
		return []byte("data: " + data + "\n\n")
	}
	return []byte(data)
}
