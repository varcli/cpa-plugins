// version_guard_test.go — the plugin self-reports the version in plugin.json.
//
// The release tooling bumps plugin.json AND the `pluginVersionLiteral` in
// main.go together, so a build must never report a version that differs from
// the manifest the registry serves. This test pins that lockstep.
package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestVersionMatchesManifest(t *testing.T) {
	raw, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Skipf("plugin.json unavailable: %v", err)
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("plugin.json: %v", err)
	}
	if doc.Version != version {
		t.Fatalf("main.go self-reports version %q but plugin.json declares %q — keep them in lockstep", version, doc.Version)
	}
}
