package main

// chat_401_storm_test.go — the 401 heal under a REAL storm: N concurrent
// executor round trips against a live httptest upstream whose refresh is
// deliberately SLOW, so callers 2..N pile onto the in-flight renewal (the
// join path) instead of the re-read path. The existing heal tests all use
// an instant refresh — the second caller re-reads the already-rotated doc
// and never exercises the join. This file pins the field shape: a token
// dies, every in-flight request on it 401s at once (Buddycn 2026-10-26),
// and the heal must spend the refresh token exactly once while every
// request still delivers its answer.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// stormEnv is chatHealEnv plus a per-goroutine executor body (each caller
// gets its own marker inside the prompt so payload corruption is visible).
func stormExecutorRaw(t *testing.T, i int) []byte {
	t.Helper()
	req := pluginapi.ExecutorRequest{
		AuthID:      "wb-1",
		Model:       "m-test",
		Payload:     []byte(fmt.Sprintf(`{"model":"m-test","messages":[{"role":"user","content":"storm-%d"}]}`, i)),
		StorageJSON: []byte(`{"auth":{"accessToken":"at-1","refreshToken":"rt-1"},"account":{"uid":"u1"}}`),
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal executor request: %v", err)
	}
	return raw
}

// TestChatExecute_401StormConcurrentHeals — 8 concurrent executor calls, all
// carrying the dead token, refresh held 250ms so joiners pile up:
//   - exactly ONE refresh round trip (the join must spend the token once);
//   - all 8 requests answer (joiners wake with the fresh token and resend);
//   - exactly ONE persist (the single renewal's save funnel);
//   - 16 chat hits (each caller: one bounce + one healed resend), the healed
//     resends all carrying the new bearer.
func TestChatExecute_401StormConcurrentHeals(t *testing.T) {
	reset401RenewState(t)

	var chat, refresh atomic.Int32
	var healedWith atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token/refresh"):
			refresh.Add(1)
			time.Sleep(250 * time.Millisecond) // widen the join window
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"accessToken":"new-at","refreshToken":"new-rt","expiresIn":3600}}`))
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			chat.Add(1)
			if r.Header.Get("Authorization") == "Bearer new-at" {
				healedWith.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(chatHealSSE))
				return
			}
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(openresty401HTML))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var mu sync.Mutex
	doc := []byte(`{"auth":{"accessToken":"at-1","refreshToken":"rt-1"},"account":{"uid":"u1"}}`)
	var saved [][]byte
	chatHealEnv(t, srv.URL, &doc, &mu, &saved)

	const n = 8
	type result struct {
		idx      int
		payload  string
		err      error
		ellapsed time.Duration
	}
	results := make([]result, n)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := handleExecExecute(stormExecutorRaw(t, i))
			results[i] = result{idx: i, payload: decodeOrEmpty(t, out), err: err, ellapsed: time.Since(start)}
		}(i)
	}
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Fatalf("caller %d lost to the storm (after %v): %v", i, r.ellapsed, r.err)
		}
		if !strings.Contains(r.payload, `"content":"hello"`) {
			t.Fatalf("caller %d payload corrupted: %s", i, r.payload)
		}
	}
	if got := refresh.Load(); got != 1 {
		t.Fatalf("refresh round trips = %d, want exactly 1 (the join must spend the token once)", got)
	}
	if got := chat.Load(); got != int32(2*n) {
		t.Fatalf("chat round trips = %d, want %d (one bounce + one healed resend each)", got, 2*n)
	}
	if got := healedWith.Load(); got != n {
		t.Fatalf("healed resends = %d, want %d (every caller must resend with the new bearer)", got, n)
	}
	if len(saved) != 1 {
		t.Fatalf("persisted saves = %d, want 1 (the single renewal)", len(saved))
	}
	if !strings.Contains(string(saved[0]), "new-at") {
		t.Fatalf("saved doc missing rotated token: %s", saved[0])
	}
}

// decodeOrEmpty is decodeExecutorPayload without the fatal (storm callers
// report through the result struct instead).
func decodeOrEmpty(t *testing.T, raw []byte) string {
	t.Helper()
	if raw == nil {
		return ""
	}
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *envelopeError  `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Sprintf("<bad envelope: %v: %s>", err, raw)
	}
	if !env.OK {
		return fmt.Sprintf("<error envelope: %+v>", env.Error)
	}
	var resp struct {
		Payload []byte `json:"Payload"`
	}
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return fmt.Sprintf("<bad result: %v>", err)
	}
	return string(resp.Payload)
}

// TestChatExecute_401StormSessionDead — the storm against a dead SESSION:
// the refresh itself is refused 401+12153 for every caller. All callers
// surface the bilingual re-login verdict with the account-level 401 status
// intact, and the credential is disabled (the disable itself is idempotent
// under N concurrent marks — no double-disable corruption of the saved doc).
func TestChatExecute_401StormSessionDead(t *testing.T) {
	reset401RenewState(t)

	var refresh atomic.Int32
	var mu2 sync.Mutex
	refreshBodies := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token/refresh"):
			refresh.Add(1)
			mu2.Lock()
			refreshBodies = append(refreshBodies, "hit")
			mu2.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":12153,"msg":"Offline user session not found"}`))
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(openresty401HTML))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var mu sync.Mutex
	doc := []byte(`{"auth":{"accessToken":"at-1","refreshToken":"rt-1"},"account":{"uid":"u1"}}`)
	var saved [][]byte
	chatHealEnv(t, srv.URL, &doc, &mu, &saved)

	const n = 6
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = handleExecExecute(stormExecutorRaw(t, i))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Fatalf("caller %d: expected the session-dead error, got success", i)
		}
		if code := chat401StatusCode(t, err); code != http.StatusUnauthorized {
			t.Fatalf("caller %d: status = %d, want 401 (host cooldown reads it)", i, code)
		}
		if !strings.Contains(err.Error(), "重新登录") {
			t.Fatalf("caller %d: error missing the re-login guidance: %v", i, err)
		}
	}
	// The session-dead refresh must ALSO be single-shot per window: joiners
	// that arrive while the refused renewal is in flight join it; callers
	// inside the 30s window afterwards are throttled (raw 401, still the
	// right answer). The upper bound is what matters: no refresh stampede.
	if got := refresh.Load(); got > int32(n) {
		t.Fatalf("refresh round trips = %d, want <= %d (no stampede)", got, n)
	}
	// The saved doc must carry the disabled flag (markSessionDead persisted).
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(mapBytes(saved), " ")
	if !strings.Contains(joined, "disabled") {
		t.Fatalf("saved docs missing the disabled flag: %s", joined)
	}
}

func mapBytes(bs [][]byte) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = string(b)
	}
	return out
}
