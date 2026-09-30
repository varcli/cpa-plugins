package provider

import _ "embed"

// panel.html is the plugin's management UI. It is served unauthenticated from
// the host resource route (/v0/resource/plugins/kiro/panel) and shares the
// visual language of the trae / qoder / workbuddy panels.
//
//go:embed panel.html
var panelHTML []byte
