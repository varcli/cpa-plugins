package provider

import (
	"bytes"
	_ "embed"
	"encoding/json"
)

//go:embed panel.html
var panelHTML []byte

// servePanel injects the host-provided management base path so the embedded
// panel can call the plugin routes without hardcoding /v0/management.
func servePanel() []byte {
	base, _ := json.Marshal(loadedManagementBasePath())
	return bytes.ReplaceAll(panelHTML, []byte("__CLINE_MANAGEMENT_BASE_PATH_JSON__"), base)
}
