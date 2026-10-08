package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// host_auth.go owns every interaction with the host's auth store: listing the
// plugin's own credential files, reading one, saving a rotated credential, and
// resolving the physical file name a rename or delete must target.

// effectiveAuthName returns the physical auth file name. The host can report a
// registered runtime name ("cline.json") while Path points at the real
// per-account file ("cline-usr-....json"); rename/delete must use the latter.
func effectiveAuthName(file hostAuthFileEntry) string {
	if base := baseName(file.Path); base != "" {
		return base
	}
	return strings.TrimSpace(file.Name)
}

// baseName is filepath.Base with the "." / "" cases folded to "".
func baseName(path string) string {
	base := strings.TrimSpace(filepath.Base(strings.TrimSpace(path)))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

func readAuthFile(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("empty auth path")
	}
	return os.ReadFile(path)
}

// hostAuthListFiles asks the host for every credential it knows about.
func hostAuthListFiles() ([]hostAuthFileEntry, error) {
	raw, err := callHostCall(pluginabi.MethodHostAuthList, map[string]any{})
	if err != nil {
		return nil, err
	}
	// callHostCall 返回的已经是宿主 envelope 里的 result 本体
	// (callHost 在 main.go 中已剥掉外层 {"ok":..,"result":..}), 这里直接解内层结构。
	var resp hostAuthListResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("host.auth.list decode result: %w", err)
	}
	return resp.Files, nil
}

// hostAuthGetByIndex reads one credential document by host auth index.
func hostAuthGetByIndex(authIndex string) ([]byte, error) {
	// 传结构化 payload: callHost 内部会 json.Marshal 一次, 传 []byte 会被编码成
	// base64 字符串, 宿主收到的是字符串而非对象 (unmarshal 失败)。
	raw, err := callHostCall(pluginabi.MethodHostAuthGet, map[string]any{"auth_index": authIndex})
	if err != nil {
		return nil, err
	}
	return decodeHostAuthGetResponse(raw)
}

// hostAuthSaveJSON writes one credential document through the host so CPA picks
// it up without a restart.
func hostAuthSaveJSON(name string, raw []byte) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("empty auth file name")
	}
	// json.RawMessage 会被 json.Marshal 原样嵌入 (不是 base64)。
	// 直接传已 Marshal 的 []byte 会让 callHost 再编码一次, 宿主收到字符串而报
	// "cannot unmarshal string into Go value of type pluginapi.HostAuthSaveRequest"。
	rawResp, err := callHostCall(pluginabi.MethodHostAuthSave, pluginapi.HostAuthSaveRequest{Name: name, JSON: json.RawMessage(raw)})
	if err != nil {
		return fmt.Errorf("host.auth.save: %w", err)
	}
	// callHostCall 已剥掉外层 envelope; 返回的是 HostAuthSaveResponse 本体。
	// 失败会以 error 形式返回 (callHost 内已把 env.Error 转成 error)。
	if len(rawResp) == 0 {
		return fmt.Errorf("host.auth.save returned no response")
	}
	return nil
}

// noteFromAuthFile returns the host-level note attached to a stored auth file.
func noteFromAuthFile(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var doc struct {
		Note string `json:"note"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(doc.Note)
}

// rewriteAuthFileNickname updates both label sources CPA can display: the
// top-level label used by the native auth list and account.nickname used by this
// plugin. The legacy note is removed because older builds wrote the custom name
// there, which made the two surfaces disagree.
func rewriteAuthFileNickname(raw []byte, nickname string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode stored auth: %w", err)
	}
	nickname = strings.TrimSpace(nickname)
	delete(doc, "note")
	account, _ := doc["account"].(map[string]any)
	if account == nil {
		account = map[string]any{}
		doc["account"] = account
	}
	if nickname != "" {
		account["nickname"] = nickname
		doc["label"] = nickname
	} else {
		delete(account, "nickname")
		doc["label"] = firstNonEmpty(stringValue(account["displayName"]), stringValue(account["email"]), providerName)
	}
	updated, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode stored auth: %w", err)
	}
	return updated, nil
}

// setAuthFileNickname persists one custom display name without dropping any
// credential fields.
func setAuthFileNickname(name string, raw []byte, nickname string) error {
	updated, err := rewriteAuthFileNickname(raw, nickname)
	if err != nil {
		return err
	}
	return hostAuthSaveJSON(name, updated)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// authFileNameFor gives each Cline account its own file so several accounts can
// coexist under one provider.
func authFileNameFor(sa *storedAuth) string {
	if sa == nil {
		return authFileName
	}
	id := strings.TrimSpace(sa.Account.ID)
	if id == "" {
		id = strings.TrimSpace(sa.Account.Email)
	}
	if id == "" {
		return authFileName
	}
	return providerName + "-" + sanitizeFileToken(id) + ".json"
}

// sanitizeFileToken reduces an account id to a file-name-safe token.
func sanitizeFileToken(value string) string {
	out := make([]rune, 0, len(value))
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	trimmed := strings.Trim(string(out), "-")
	if trimmed == "" {
		return "account"
	}
	if len(trimmed) > 32 {
		trimmed = trimmed[:32]
	}
	return trimmed
}

// buildAuthFileJSON renders the physical credential file: nested storage under
// auth/account plus the host-level metadata CPA renders in the auth list.
func buildAuthFileJSON(sa *storedAuth) ([]byte, error) {
	if sa == nil {
		return nil, fmt.Errorf("nil stored auth")
	}
	storage, err := json.Marshal(sa)
	if err != nil {
		return nil, err
	}
	var nested map[string]any
	if err := json.Unmarshal(storage, &nested); err != nil {
		return nil, err
	}
	out := map[string]any{
		"type":     providerName,
		"provider": providerName,
		"disabled": false,
		"label":    clineAccountLabel(sa),
		"auth":     nested["auth"],
		"account":  nested["account"],
	}
	if email := strings.TrimSpace(sa.Account.Email); email != "" {
		out["email"] = email
	}
	return json.Marshal(out)
}

// clineAccountLabel mirrors the label CPA shows for a Cline credential. It is
// persisted at the physical-file top level because the file token store reads
// that map, not the nested account record.
func clineAccountLabel(sa *storedAuth) string {
	if sa == nil {
		return providerName
	}
	if label := strings.TrimSpace(sa.Account.Nickname); label != "" {
		return label
	}
	if label := strings.TrimSpace(sa.Account.DisplayName); label != "" {
		return label
	}
	if label := strings.TrimSpace(sa.Account.Email); label != "" {
		return label
	}
	return providerName
}

// persistAuthData writes an auth produced by the OAuth flow to the host auth
// directory so CPA loads it without a restart.
func persistAuthData(auth pluginapi.AuthData) error {
	sa, err := parseStored(auth.StorageJSON)
	if err != nil {
		return err
	}
	fileJSON, err := buildAuthFileJSON(sa)
	if err != nil {
		return err
	}
	return hostAuthSaveJSON(authFileNameFor(sa), fileJSON)
}

// findOwnAuthFile resolves an auth index belonging to this provider and returns
// both the file entry and its raw JSON.
func findOwnAuthFile(authIndex string) (hostAuthFileEntry, []byte, error) {
	files, err := hostAuthListFiles()
	if err != nil {
		return hostAuthFileEntry{}, nil, fmt.Errorf("host auth list unavailable")
	}
	for _, file := range files {
		if !strings.EqualFold(strings.TrimSpace(file.AuthIndex), strings.TrimSpace(authIndex)) {
			continue
		}
		if !isOwnAuthFile(file) {
			return hostAuthFileEntry{}, nil, fmt.Errorf("auth %s does not belong to %s", authIndex, providerName)
		}
		raw, errGet := hostAuthGetByIndex(file.AuthIndex)
		if errGet != nil {
			return hostAuthFileEntry{}, nil, fmt.Errorf("host auth get failed")
		}
		return file, raw, nil
	}
	return hostAuthFileEntry{}, nil, fmt.Errorf("auth %s not found", authIndex)
}

// isOwnAuthFile reports whether an auth entry belongs to this provider.
//
// Do not use the host callback's Name field as the source of truth: the host
// reports the registered credential name (for Cline that is "cline.json") even
// when the physical auth file is named "cline-<account>.json". Filtering by Name
// therefore drops every Cline account. Provider/type are populated by the host
// from the runtime credential and stay correct for both old and new files; keep
// the filename fallback for the disk-only callback path, which does not always
// populate provider metadata.
func isOwnAuthFile(file hostAuthFileEntry) bool {
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(file.Provider, file.Type)))
	if provider != "" {
		return provider == providerName
	}
	name := strings.ToLower(effectiveAuthName(file))
	if name == "" {
		return false
	}
	if name == authFileName {
		return true
	}
	return strings.HasPrefix(name, providerName+"-")
}

// ownStoredAuths reads the plugin's own auth files through the host.
//
// Split out from the catalog merge because it is the only part that needs a
// running CPA host, which leaves the merge testable on its own.
func ownStoredAuths() ([]*storedAuth, error) {
	files, err := hostAuthListFiles()
	if err != nil {
		return nil, err
	}
	accounts := make([]*storedAuth, 0, len(files))
	for _, file := range files {
		if !isOwnAuthFile(file) {
			continue
		}
		raw, errGet := hostAuthGetByIndex(file.AuthIndex)
		if errGet != nil {
			continue
		}
		sa, errParse := parseStored(raw)
		if errParse != nil {
			continue
		}
		accounts = append(accounts, sa)
	}
	return accounts, nil
}
