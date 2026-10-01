package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// v0.5.3: the registration metadata is what the CPA management UI reads to
// render the plugin's sidebar menu entry. A logo that does not resolve renders
// as a blank icon, which is exactly what happened when the value pointed at a
// blob that 404s.
//
// The host serves plugin resource routes by exact path match only — it never
// forwards /v0/resource/plugins/<id>/<file> sub-paths — so a relative or
// self-hosted icon path can never be fetched. The logo must be an absolute URL.
func TestRegistrationLogoIsFetchableAbsoluteURL(t *testing.T) {
	if !strings.HasPrefix(pluginLogoURL, "https://") {
		t.Fatalf("pluginLogoURL must be an absolute https URL the browser can fetch directly, got %q", pluginLogoURL)
	}
	// The old value was a dead blob. Pin the host so a future edit cannot
	// silently reintroduce a third-party path that may disappear.
	if !strings.Contains(pluginLogoURL, "kiro.dev") {
		t.Errorf("pluginLogoURL should point at Kiro's own published icon, got %q", pluginLogoURL)
	}

	raw, errRegistration := registration([]byte(`{}`))
	if errRegistration != nil {
		t.Fatalf("registration: %v", errRegistration)
	}
	var env envelope
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil || !env.OK {
		t.Fatalf("bad envelope: %s", raw)
	}
	var payload struct {
		Metadata struct {
			Name string `json:"name"`
			Logo string `json:"logo"`
		} `json:"metadata"`
	}
	if errDecode := json.Unmarshal(env.Result, &payload); errDecode != nil {
		t.Fatalf("bad registration result: %s", env.Result)
	}
	if payload.Metadata.Logo != pluginLogoURL {
		t.Fatalf("registration advertises logo %q, want %q", payload.Metadata.Logo, pluginLogoURL)
	}
}

// The panel resource must still be registered with a menu label — that label
// is what the management UI shows next to the icon, so losing it would make the
// entry unclickable in the sidebar regardless of the icon.
func TestPanelResourceAdvertisesMenuLabel(t *testing.T) {
	raw, errRegistration := registerManagement()
	if errRegistration != nil {
		t.Fatalf("registerManagement: %v", errRegistration)
	}
	var env envelope
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil || !env.OK {
		t.Fatalf("bad envelope: %s", raw)
	}
	var payload struct {
		Resources []struct {
			Path string `json:"path"`
			Menu string `json:"menu"`
		} `json:"resources"`
	}
	if errDecode := json.Unmarshal(env.Result, &payload); errDecode != nil {
		t.Fatalf("bad registration result: %s", env.Result)
	}
	var found bool
	for _, resource := range payload.Resources {
		if resource.Path == "/panel" {
			found = true
			if strings.TrimSpace(resource.Menu) == "" {
				t.Fatal("面板资源必须带 menu 标签，否则侧栏菜单项没有文字")
			}
		}
	}
	if !found {
		t.Fatalf("面板资源未注册: %s", env.Result)
	}
}
