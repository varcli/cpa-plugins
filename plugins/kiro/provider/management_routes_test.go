package provider

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// CPA registers its own gin routes under /v0/management/plugins/:id/* for the
// credential-quota feature. Gin resolves those before the plugin host's NoRoute
// fallback, so a plugin route that lands on the same literal path never runs:
// the browser gets the host handler's error instead (observed as
// "加载失败：auth_index is required" on GET /plugins/kiro/quota).
//
// The host's reserved-route guard does not save us either — it compares literal
// paths, and "/plugins/:id/quota" != "/plugins/kiro/quota", so the collision is
// silent. This test is the warning the host does not give.
//
// Keep this list in sync with internal/api/server_management*.go in CLIProxyAPI.
var hostReservedPluginSuffixes = []string{
	"quota",       // GET|POST|DELETE /plugins/:id/quota
	"quota/reset", // POST  /plugins/:id/quota/reset
	"config",      // GET|PUT|PATCH /plugins/:id/config
	"enabled",     // PATCH /plugins/:id/enabled
	"",            // DELETE /plugins/:id
}

// A suffix under /plugins/:id/ that the host also owns must never be declared,
// or the plugin's handler is dead on arrival.
func TestManagementRoutesAvoidHostReservedSuffixes(t *testing.T) {
	raw, errRegistration := registerManagement()
	if errRegistration != nil {
		t.Fatalf("registerManagement: %v", errRegistration)
	}
	var env envelope
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil || !env.OK {
		t.Fatalf("bad envelope: %s", raw)
	}
	var payload managementRegistrationResponse
	if errDecode := json.Unmarshal(env.Result, &payload); errDecode != nil {
		t.Fatalf("bad registration result: %s", env.Result)
	}

	prefix := "/plugins/" + providerName + "/"
	for _, route := range payload.Routes {
		path := strings.TrimSpace(route.Path)
		if !strings.HasPrefix(path, prefix) {
			t.Errorf("route %s %s is not under %s", route.Method, path, prefix)
			continue
		}
		suffix := strings.TrimPrefix(path, prefix)
		for _, reserved := range hostReservedPluginSuffixes {
			if reserved == "" {
				continue
			}
			// Reserved here means "the host serves this exact literal path".
			// A longer plugin path such as "quotaRequest" is fine.
			if suffix != reserved && !strings.HasPrefix(suffix, reserved+"/") {
				continue
			}
			t.Errorf("route %s %s collides with the host's own /plugins/:id/%s gin route; "+
				"gin matches it before NoRoute so this handler can never run — rename it",
				route.Method, path, reserved)
		}
	}
}

// The list endpoint the panel polls is the one the collision broke. Pin both
// the name and the fact that GET is the accepted method, so a rename back to
// "/quota" fails here rather than in the browser.
func TestUsageRouteRegisteredForGet(t *testing.T) {
	raw, errRegistration := registerManagement()
	if errRegistration != nil {
		t.Fatalf("registerManagement: %v", errRegistration)
	}
	var env envelope
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil || !env.OK {
		t.Fatalf("bad envelope: %s", raw)
	}
	var payload managementRegistrationResponse
	if errDecode := json.Unmarshal(env.Result, &payload); errDecode != nil {
		t.Fatalf("bad registration result: %s", env.Result)
	}

	want := "/plugins/" + providerName + "/usage"
	var found bool
	for _, route := range payload.Routes {
		if route.Path == want {
			found = true
			if !strings.EqualFold(route.Method, http.MethodGet) {
				t.Fatalf("route %s must accept GET, got %s", want, route.Method)
			}
		}
	}
	if !found {
		t.Fatalf("list endpoint %s is not registered: %s", want, env.Result)
	}
}

// The dispatcher must actually serve the list endpoint, not merely register it.
// This is the end-to-end assertion the browser failure reduced to: a GET on the
// declared path returns 200 with an accounts array, rather than falling through
// to the host's "auth_index is required".
func TestUsageRouteDispatchesToQuotaList(t *testing.T) {
	base := loadedManagementBasePath() + "/plugins/" + providerName

	original := callHostCall
	callHostCall = func(method string, payload any) (json.RawMessage, error) {
		if method != "host.auth.list" {
			// No credentials are seeded, so nothing else should be reached.
			t.Errorf("unexpected host call %q", method)
		}
		return json.RawMessage(`{"files":[]}`), nil
	}
	t.Cleanup(func() { callHostCall = original })

	raw, errHandle := handleManagement([]byte(`{"Method":"GET","Path":"` + base + `/usage"}`))
	if errHandle != nil {
		t.Fatalf("handleManagement: %v", errHandle)
	}
	status, payload := decodeManagementBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("GET %s/usage status = %d, want 200 (payload %v)", base, status, payload)
	}
	if _, ok := payload["accounts"]; !ok {
		t.Fatalf("GET %s/usage body has no accounts key: %v", base, payload)
	}
}
