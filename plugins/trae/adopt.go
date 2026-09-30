// adopt.go migrates legacy trae-cn / trae-solo-cn / trae-intl auth files to
// the merged trae provider (v0.12.0).
//
// The three legacy plugins ran against DIFFERENT upstreams (api.trae.cn
// inline_chat, api.trae.cn solo_work_lite, api.marscode.com chat_sessions),
// so there is NO dedupe across them. Filenames are KEPT — trae-cn-<uid>.json,
// trae-solo-cn-<uid>.json and trae-intl-<uid>.json all carry the canonical
// "trae-" prefix — and only the JSON type/provider is rewritten in place:
//
//	type/provider: trae-cn | trae-solo-cn | trae-intl → trae
//	auth.variant:  cn | solo | intl (derived from the filename)
//
// The host routes auth files to plugins by the file's type field, so without
// this rewrite legacy files would be orphaned. Idempotent, 1-minute guard,
// runs in the background.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

var (
	adoptMu      sync.Mutex
	lastAdoptRun time.Time
)

// startAdoption kicks the background migration (register/reconfigure safe).
//
// v0.12.68 (issue #22 mirror): during plugin registration the host auth
// manager may not be wired yet — host.auth.list succeeds via its directory
// fallback but host.auth.get answers "bad envelope" for entries the manager
// has not indexed, and adoption silently never ran. Retry until the
// manager-backed view is available; the direct-by-path fallback inside
// adoptForeignAuths makes each attempt independent of manager readiness.
func startAdoption() {
	go func() {
		adoptMu.Lock()
		if !lastAdoptRun.IsZero() && time.Since(lastAdoptRun) < time.Minute {
			adoptMu.Unlock()
			return
		}
		lastAdoptRun = time.Now()
		adoptMu.Unlock()
		for attempt := 0; attempt < 10; attempt++ {
			if attempt > 0 {
				time.Sleep(2 * time.Second)
			}
			if adoptForeignAuths() {
				return
			}
		}
		log.Printf("adopt: host auth manager did not become ready; will retry on next reload")
	}()
}

// legacyTraeVariant maps a legacy file name to its variant ("" = not legacy).
func legacyTraeVariant(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasPrefix(lower, "trae-solo-cn-"), lower == "trae-solo-cn.json":
		return variantSolo
	case strings.HasPrefix(lower, "trae-intl-"), lower == "trae-intl.json":
		return variantIntl
	case strings.HasPrefix(lower, "trae-cn-"), lower == "trae-cn.json":
		return variantCN
	}
	return ""
}

// adoptForeignAuths rewrites every legacy trae auth file in place. Returns
// true when every legacy candidate was resolvable (or none exist), false
// when the host still needs time to publish auth indexes (caller retries).
func adoptForeignAuths() bool {
	files, err := hostAuthList()
	if err != nil {
		log.Printf("adopt: host auth list failed: %v", err)
		return false
	}
	adopted := 0
	waitingForHost := false
	for _, f := range files {
		variant := legacyTraeVariant(f.Name)
		if variant == "" {
			continue
		}
		raw, err := hostAuthGetRaw(f.AuthIndex)
		if err != nil || len(raw) == 0 {
			// Manager not ready (bad envelope / empty index): the list entry
			// carries the backing path — read the physical file directly
			// (issue #22 mirror).
			if diskRaw := readPhysicalByPath(f.Path); len(diskRaw) > 0 {
				raw = json.RawMessage(diskRaw)
				err = nil
			}
		}
		if err != nil || len(raw) == 0 {
			log.Printf("adopt %s: get failed: %v", f.Name, err)
			waitingForHost = true
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &probe)
		if strings.EqualFold(strings.TrimSpace(probe.Type), providerName) {
			continue // already canonical
		}
		// For cn/solo files, refresh the variant field inside the nested auth
		// object so auth.Parse picks it up. Intl files keep their native shape
		// (the intl parser reads it via sniffVariantFromJSON).
		if variant != variantIntl {
			raw = injectVariant(raw, variant)
			if raw == nil {
				continue
			}
		}
		// Rewrite type/provider, preserving everything else.
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			log.Printf("adopt %s: unparsable — left in place", f.Name)
			continue
		}
		doc["type"] = providerName
		doc["provider"] = providerName
		out, err := json.Marshal(doc)
		if err != nil {
			log.Printf("adopt %s: encode failed: %v", f.Name, err)
			continue
		}
		if err := hostAuthSave(f.Name, out); err != nil {
			log.Printf("adopt %s: save failed: %v", f.Name, err)
			continue
		}
		log.Printf("adopt %s: type→%s, variant=%s", f.Name, providerName, variant)
		adopted++
	}
	if adopted > 0 {
		log.Printf("adopt: done — migrated %d legacy trae auth file(s)", adopted)
	}
	return !waitingForHost
}

// readPhysicalByPath reads the credential document straight from the
// host.auth.list entry's backing path. Best-effort: empty path or unreadable
// file returns nil so the caller's retry ladder takes over.
func readPhysicalByPath(path string) []byte {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(abs)
	if err != nil || len(raw) == 0 {
		return nil
	}
	return raw
}

// hostAuthGetRaw fetches one credential's raw JSON from the host auth
// store (mirrors hostAuthGet but returns the unparsed JSON).
func hostAuthGetRaw(authIndex string) (json.RawMessage, error) {
	reqBody, _ := json.Marshal(map[string]string{"auth_index": authIndex})
	raw, err := hostCall(pluginabi.MethodHostAuthGet, reqBody)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil, fmt.Errorf("host.auth.get: bad envelope")
	}
	var resp rpcHostAuthGetResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil, err
	}
	return resp.JSON, nil
}

// injectVariant sets auth.variant in a nested-shape auth file JSON.
func injectVariant(raw []byte, variant string) []byte {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		log.Printf("adopt: inject variant: unparsable file")
		return nil
	}
	authObj, _ := doc["auth"].(map[string]any)
	if authObj == nil {
		// flat shape — store at top level
		doc["variant"] = variant
	} else {
		authObj["variant"] = variant
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	return out
}
