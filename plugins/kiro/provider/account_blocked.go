package provider

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

const (
	blockedAccountMessage = "User is not authorized to make this call"
	blockedNote           = "Kiro 拒绝了该账号（HTTP 403），可能已被封禁；确认解封后请手动启用"
	blockedAtKey          = "kiro_blocked_at"
)

func isBlockedResponse(status int, body []byte) bool {
	return status == 403 && strings.Contains(string(body), blockedAccountMessage)
}

func isBlockedError(err error) bool {
	typed, ok := err.(statusError)
	if !ok {
		return false
	}
	return isBlockedResponse(typed.HTTPStatus, []byte(typed.Body+" "+typed.Message))
}

func blockedStorage(storageJSON []byte, cred credential, now time.Time) []byte {
	fields := map[string]any{}
	if len(storageJSON) > 0 {
		_ = json.Unmarshal(storageJSON, &fields)
	}
	if freshJSON, errMarshal := json.Marshal(cred); errMarshal == nil {
		var fresh map[string]any
		if json.Unmarshal(freshJSON, &fresh) == nil {
			for key, value := range fresh {
				fields[key] = value
			}
		}
	}
	fields["disabled"] = true
	fields[blockedAtKey] = now.UTC().Format(time.RFC3339)
	if note, _ := fields["note"].(string); strings.TrimSpace(note) == "" || note == blockedNote {
		fields["note"] = blockedNote
	}
	raw, _ := json.Marshal(fields)
	return raw
}

func disableCredentialBestEffort(authID, path string, storageJSON []byte, cred credential) {
	raw := blockedStorage(storageJSON, cred, time.Now().UTC())
	name := filepath.Base(strings.TrimSpace(path))
	if name == "." || name == "" {
		candidate := filepath.Base(strings.TrimSpace(authID))
		if strings.HasSuffix(strings.ToLower(candidate), ".json") {
			name = candidate
		} else {
			name = cred.fileName()
		}
	}
	_, _ = callHostCall("host.auth.save", map[string]any{"name": name, "json": json.RawMessage(raw)})
}
