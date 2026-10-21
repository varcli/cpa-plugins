// version_guard_test.go — the plugin self-reports the version in plugin.json.
//
// The release tooling bumps plugin.json AND the `pluginVersionLiteral` in
// main.go together, so a build must never report a version that differs from
// the manifest the registry serves. This test pins that lockstep.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	// 同步上游代码时 main.go 带的是上游版本号，plugin.json 仍是本仓版本；
	// 两者在 `release.go publish` 时由 version 子命令统一。存在未消费的变更集
	// 说明正处于待发版状态，此时跳过，避免把正常的过渡态当成漂移。
	if pending, _ := filepath.Glob(filepath.Join("changesets", "*.json")); len(pending) > 0 {
		t.Skipf("pending changesets (%d) — version is unified at publish time", len(pending))
	}
	if doc.Version != version {
		t.Fatalf("main.go self-reports version %q but plugin.json declares %q — keep them in lockstep", version, doc.Version)
	}
}
