package provider

// host_auth_test.go pins the host.auth.save / host.auth.get payload encoding.
//
// Regression (field report): hostAuthSaveJSON marshalled the request into a
// []byte and handed that to callHostCall. The host callback marshals the
// payload AGAIN, and encoding/json renders a []byte as a base64 STRING — so the
// host received a JSON string instead of an object and rejected it with:
//
//   host.auth.save: host_call_failed: decode host auth save request:
//   json: cannot unmarshal string into Go value of type
//   pluginapi.HostAuthSaveRequest
//
// The fix passes a struct / map (with json.RawMessage for the credential
// bytes) so the single marshal inside the callback produces an object. This
// test reproduces the double-marshal contract: the fake caller marshals the
// payload exactly like the real callback does, then decodes it back into the
// host-side request type.

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

// TestHostAuthSavePayloadIsAnObject is the regression gate: the payload the
// plugin hands to the callback must survive one more json.Marshal as an OBJECT
// (not a base64 string), so the host can unmarshal it into
// pluginapi.HostAuthSaveRequest.
func TestHostAuthSavePayloadIsAnObject(t *testing.T) {
	credential := []byte(`{"accessToken":"at-123","refreshToken":"rt-456"}`)
	var sawRequest pluginapi.HostAuthSaveRequest

	withCallHostCall(t, func(method string, payload any) (json.RawMessage, error) {
		if method != "host.auth.save" {
			t.Fatalf("unexpected method %q", method)
		}
		// Reproduce the real callback: marshal the payload, then unmarshal it
		// into the host-side request type. A []byte payload would marshal to a
		// base64 string and this unmarshal would fail exactly like the host.
		wire, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		if len(wire) > 0 && wire[0] == '"' {
			t.Fatalf("payload marshalled to a JSON string, not an object: %s", wire)
		}
		if err := json.Unmarshal(wire, &sawRequest); err != nil {
			t.Fatalf("host would reject the payload: %v (wire=%s)", err, wire)
		}
		return json.RawMessage(`{"ok":true,"result":{}}`), nil
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

// TestHostAuthGetPayloadIsAnObject covers the sibling call: the get request
// must also reach the host as an object (it had the same []byte defect).
func TestHostAuthGetPayloadIsAnObject(t *testing.T) {
	withCallHostCall(t, func(method string, payload any) (json.RawMessage, error) {
		if method != "host.auth.get" {
			t.Fatalf("unexpected method %q", method)
		}
		wire, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		if len(wire) > 0 && wire[0] == '"' {
			t.Fatalf("payload marshalled to a JSON string, not an object: %s", wire)
		}
		var req struct {
			AuthIndex string `json:"auth_index"`
		}
		if err := json.Unmarshal(wire, &req); err != nil {
			t.Fatalf("host would reject the payload: %v (wire=%s)", err, wire)
		}
		if req.AuthIndex != "idx-9" {
			t.Errorf("auth_index = %q, want idx-9", req.AuthIndex)
		}
		return json.RawMessage(`{"ok":true,"result":{"json":{"accessToken":"at"}}}`), nil
	})

	if _, err := hostAuthGetByIndex("idx-9"); err != nil {
		t.Fatalf("hostAuthGetByIndex: %v", err)
	}
}
