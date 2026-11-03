package provider

import (
	"encoding/json"
	"net/http"
	"strings"
)

type credentialImportRequest struct {
	AuthMethod     string   `json:"auth_method"`
	RefreshToken   string   `json:"refresh_token"`
	ClientID       string   `json:"client_id"`
	ClientSecret   string   `json:"client_secret"`
	TokenEndpoint  string   `json:"token_endpoint"`
	Region         string   `json:"region"`
	APIRegion      string   `json:"api_region"`
	SocialProvider string   `json:"social_provider"`
	KiroAPIKey     string   `json:"kiro_api_key"`
	Scopes         []string `json:"scopes"`
	Label          string   `json:"label"`
	DisableCooling bool     `json:"disable_cooling"`
}

const maxCredentialField = 64 << 10

func handleCredentialImport(req managementRequest) ([]byte, error) {
	if req.Method != http.MethodPost {
		return managementJSON(http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"}), nil
	}
	var body credentialImportRequest
	if len(req.Body) == 0 || json.Unmarshal(req.Body, &body) != nil {
		return managementJSON(http.StatusBadRequest, map[string]any{"error": "invalid_request"}), nil
	}
	if len(body.RefreshToken) > maxCredentialField || len(body.ClientSecret) > maxCredentialField || len(body.KiroAPIKey) > maxCredentialField {
		return managementJSON(http.StatusRequestEntityTooLarge, map[string]any{"error": "credential_too_large"}), nil
	}
	cred, errCredential := importedCredential(body)
	if errCredential != nil {
		return managementJSON(http.StatusBadRequest, map[string]any{"error": "invalid_credential", "message": errCredential.Error()}), nil
	}

	warning := ""
	if cred.AuthMethod != authMethodAPIKey {
		refreshed, errRefresh := refreshCredential(cred, req.HostCallbackID)
		if errRefresh != nil {
			warning = "凭据已保存，但首次刷新失败：" + errRefresh.Error()
		} else {
			cred = finalizeLoginCredential(refreshed, req.HostCallbackID)
		}
	}
	auth, errAuth := authDataFromCredential(cred)
	if errAuth != nil {
		return managementJSON(http.StatusBadGateway, map[string]any{"error": "auth_encode_failed", "message": errAuth.Error()}), nil
	}
	if _, errSave := callHostCall("host.auth.save", map[string]any{"name": auth.FileName, "json": json.RawMessage(auth.StorageJSON)}); errSave != nil {
		return managementJSON(http.StatusBadGateway, map[string]any{"error": "auth_save_failed", "message": errSave.Error()}), nil
	}
	response := map[string]any{
		"status":      "success",
		"id":          auth.ID,
		"file_name":   auth.FileName,
		"label":       auth.Label,
		"auth_method": cred.AuthMethod,
	}
	if cred.Email != "" {
		response["email"] = cred.Email
	}
	if warning != "" {
		response["warning"] = warning
	}
	return managementJSON(http.StatusOK, response), nil
}

func importedCredential(body credentialImportRequest) (credential, error) {
	method := normalizeAuthMethod(body.AuthMethod, map[string]any{})
	region := firstNonEmpty(body.Region, defaultRegion)
	apiRegion := firstNonEmpty(body.APIRegion, region)
	if errRegion := validateOrganizationParameters("https://view.awsapps.com/start", region); errRegion != nil {
		return credential{}, errString("Region 格式不正确")
	}
	if errRegion := validateOrganizationParameters("https://view.awsapps.com/start", apiRegion); errRegion != nil {
		return credential{}, errString("API Region 格式不正确")
	}
	cred := credential{
		Version:        1,
		AuthMethod:     method,
		Mode:           "copy",
		SourceKind:     "management_import",
		RefreshToken:   strings.TrimSpace(body.RefreshToken),
		ClientID:       strings.TrimSpace(body.ClientID),
		ClientSecret:   strings.TrimSpace(body.ClientSecret),
		TokenEndpoint:  strings.TrimSpace(body.TokenEndpoint),
		SSORegion:      region,
		APIRegion:      apiRegion,
		SocialProvider: strings.ToLower(strings.TrimSpace(body.SocialProvider)),
		KiroAPIKey:     strings.TrimSpace(body.KiroAPIKey),
		Scopes:         append([]string(nil), body.Scopes...),
		Label:          strings.TrimSpace(body.Label),
		DisableCooling: body.DisableCooling,
	}
	if cred.AuthMethod == authMethodSocial && cred.SocialProvider != "google" && cred.SocialProvider != "github" {
		cred.SocialProvider = ""
	}
	finalizeCredential(&cred)

	switch cred.AuthMethod {
	case authMethodAPIKey:
		if cred.KiroAPIKey == "" {
			return credential{}, errString("API Key 不能为空")
		}
	case authMethodSocial:
		if cred.RefreshToken == "" {
			return credential{}, errString("Refresh Token 不能为空")
		}
	case authMethodIDC:
		if cred.RefreshToken == "" || cred.ClientID == "" || cred.ClientSecret == "" {
			return credential{}, errString("IdC 凭据需要 Refresh Token、Client ID 和 Client Secret")
		}
	case authMethodExternalIDP:
		if cred.RefreshToken == "" || cred.ClientID == "" {
			return credential{}, errString("External IdP 凭据需要 Refresh Token 和 Client ID")
		}
		if errEndpoint := validateTokenEndpoint(cred.TokenEndpoint); errEndpoint != nil {
			return credential{}, errEndpoint
		}
	}
	return cred, nil
}

type errString string

func (e errString) Error() string { return string(e) }
