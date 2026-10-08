package provider

// host_auth_test.go pins the host.auth.* callback contract.
//
// There are TWO layers on the host-callback path and it is easy to get the
// boundary wrong, so this file pins both:
//
//  1. PAYLOAD (outbound): the plugin hands a structured value to callHostCall.
//     The C ABI layer (main.go callHost) marshals it once more, and
//     encoding/json renders a []byte as a base64 STRING — so passing an
//     already-marshalled []byte made the host reject the request with
//     "json: cannot unmarshal string into Go value of type
//     pluginapi.HostAuthSaveRequest".
//
//  2. RESULT (inbound): main.go callHost unwraps the host envelope and returns
//     ONLY the inner "result" payload. A caller that unmarshals the return value
//     as an envelope therefore always fails ("host.auth.save failed").
//
// Regression (field reports, 2026-10-08): host_auth.go had BOTH defects — the
// outbound []byte double-encode and the inbound redundant envelope decode.

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// withCallHostCall swaps the host-call indirection and restores it after.
func withCallHostCall(t *testing.T, fn func(string, any) (json.RawMessage, error)) {
	t.Helper()
	previous := callHostCall
	callHostCall = fn
	t.Cleanup(func() { callHostCall = previous })
}

// outboundWire reproduces the C ABI layer: marshal the payload once, exactly
// like main.go callHost does before handing the bytes to the host.
func outboundWire(t *testing.T, payload any) []byte {
	t.Helper()
	wire, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if len(wire) > 0 && wire[0] == '"' {
		t.Fatalf("payload marshalled to a JSON string, not an object: %s", wire)
	}
	return wire
}

// TestHostAuthSavePayloadIsAnObject covers layer 1: the outbound payload must
// survive one more json.Marshal as an OBJECT so the host can unmarshal it into
// pluginapi.HostAuthSaveRequest.
func TestHostAuthSavePayloadIsAnObject(t *testing.T) {
	credential := []byte(`{"accessToken":"at-123","refreshToken":"rt-456"}`)
	var sawRequest pluginapi.HostAuthSaveRequest

	withCallHostCall(t, func(method string, payload any) (json.RawMessage, error) {
		if method != "host.auth.save" {
			t.Fatalf("unexpected method %q", method)
		}
		wire := outboundWire(t, payload)
		if err := json.Unmarshal(wire, &sawRequest); err != nil {
			t.Fatalf("host would reject the payload: %v (wire=%s)", err, wire)
		}
		// Layer 2: the callback returns the INNER result only (main.go callHost
		// unwraps the envelope). Mirrors HostAuthSaveResponse.
		return json.RawMessage(`{"name":"cline-usr-1.json","path":"/tmp/cline-usr-1.json"}`), nil
	})

	if err := hostAuthSaveJSON("cline-usr-1.json", credential); err != nil {
		t.Fatalf("hostAuthSaveJSON: %v", err)
	}
	if sawRequest.Name != "cline-usr-1.json" {
		t.Errorf("Name = %q, want cline-usr-1.json", sawRequest.Name)
	}
	if string(sawRequest.JSON) != string(credential) {
		t.Errorf("JSON = %s, want %s", sawRequest.JSON, credential)
	}
}

// TestHostAuthSaveDoesNotReDecodeEnvelope covers layer 2 for save: the value
// returned by the callback is already the inner result, so re-unmarshalling it
// as an envelope must NOT be attempted. A result without "ok" is a SUCCESS.
func TestHostAuthSaveDoesNotReDecodeEnvelope(t *testing.T) {
	withCallHostCall(t, func(string, any) (json.RawMessage, error) {
		// No "ok"/"result" wrapper — exactly what callHost returns.
		return json.RawMessage(`{"name":"cline-usr-2.json","path":"/tmp/cline-usr-2.json"}`), nil
	})
	if err := hostAuthSaveJSON("cline-usr-2.json", []byte(`{"accessToken":"at"}`)); err != nil {
		t.Fatalf("save must accept a bare result payload, got: %v", err)
	}
}

// TestHostAuthListPayloadIsAnObjectAndResultIsInner mirrors both layers for the
// list call.
func TestHostAuthListPayloadIsAnObjectAndResultIsInner(t *testing.T) {
	withCallHostCall(t, func(method string, payload any) (json.RawMessage, error) {
		if method != "host.auth.list" {
			t.Fatalf("unexpected method %q", method)
		}
		outboundWire(t, payload)
		return json.RawMessage(`{"files":[{"name":"cline-a.json","auth_index":"idx-1"}]}`), nil
	})
	files, err := hostAuthListFiles()
	if err != nil {
		t.Fatalf("hostAuthListFiles: %v", err)
	}
	if len(files) != 1 || files[0].Name != "cline-a.json" {
		t.Fatalf("files = %+v, want one cline-a.json", files)
	}
}

// TestHostAuthGetPayloadIsAnObjectAndResultIsInner covers the get call on both
// layers: the outbound request must be an object and the inbound value is the
// inner HostAuthGetResponse (no envelope wrapper).
func TestHostAuthGetPayloadIsAnObjectAndResultIsInner(t *testing.T) {
	withCallHostCall(t, func(method string, payload any) (json.RawMessage, error) {
		if method != "host.auth.get" {
			t.Fatalf("unexpected method %q", method)
		}
		wire := outboundWire(t, payload)
		var req struct {
			AuthIndex string `json:"auth_index"`
		}
		if err := json.Unmarshal(wire, &req); err != nil {
			t.Fatalf("host would reject the payload: %v (wire=%s)", err, wire)
		}
		if req.AuthIndex != "idx-9" {
			t.Errorf("auth_index = %q, want idx-9", req.AuthIndex)
		}
		return json.RawMessage(`{"auth_index":"idx-9","name":"cline-9.json","json":{"accessToken":"at-9"}}`), nil
	})

	got, err := hostAuthGetByIndex("idx-9")
	if err != nil {
		t.Fatalf("hostAuthGetByIndex: %v", err)
	}
	if string(got) != `{"accessToken":"at-9"}` {
		t.Errorf("credential = %s, want {\"accessToken\":\"at-9\"}", got)
	}
}
