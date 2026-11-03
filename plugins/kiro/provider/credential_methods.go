package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

const (
	authMethodSocial      = "social"
	authMethodIDC         = "idc"
	authMethodExternalIDP = "external_idp"
	authMethodAPIKey      = "api_key"
)

func normalizeAuthMethod(value string, data map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "api_key", "apikey", "api-key":
		return authMethodAPIKey
	case "external_idp", "external-idp", "externalidp", "azuread", "entra", "entra_id":
		return authMethodExternalIDP
	case "idc", "builder-id", "builderid", "iam", "aws_sso_oidc":
		return authMethodIDC
	case "social", "google", "github":
		return authMethodSocial
	}
	if kironxString(data, "kiroApiKey", "kiro_api_key") != "" {
		return authMethodAPIKey
	}
	if kironxString(data, "tokenEndpoint", "token_endpoint") != "" {
		return authMethodExternalIDP
	}
	if kironxString(data, "clientId", "client_id") != "" && kironxString(data, "clientSecret", "client_secret") != "" {
		return authMethodIDC
	}
	return authMethodSocial
}

func normalizeExplicitAuthMethod(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "api_key", "apikey", "api-key":
		return authMethodAPIKey
	case "external_idp", "external-idp", "externalidp", "azuread", "entra", "entra_id":
		return authMethodExternalIDP
	case "idc", "builder-id", "builderid", "iam", "aws_sso_oidc":
		return authMethodIDC
	case "social", "google", "github":
		return authMethodSocial
	default:
		return ""
	}
}

func (c credential) bearerToken() string {
	if c.AuthMethod == authMethodAPIKey {
		return strings.TrimSpace(c.KiroAPIKey)
	}
	return strings.TrimSpace(c.AccessToken)
}

func (c credential) tokenType() string {
	switch c.AuthMethod {
	case authMethodAPIKey:
		return "API_KEY"
	case authMethodExternalIDP:
		return "EXTERNAL_IDP"
	default:
		return ""
	}
}

func (c credential) displayMethod() string {
	switch c.AuthMethod {
	case authMethodAPIKey:
		return "API Key"
	case authMethodExternalIDP:
		return "External IdP"
	case authMethodIDC:
		if strings.Contains(strings.ToLower(c.Label), "builder") || strings.EqualFold(c.Provider, "BuilderId") {
			return "Builder ID"
		}
		return "IAM Identity Center"
	case authMethodSocial:
		switch strings.ToLower(strings.TrimSpace(c.SocialProvider)) {
		case "google":
			return "Google"
		case "github":
			return "GitHub"
		}
		return "Social"
	default:
		return "Kiro"
	}
}

func (c credential) fileIdentity() string {
	if c.Email != "" {
		return safeFileToken(c.Email)
	}
	if c.UserID != "" {
		return safeFileToken(c.UserID)
	}
	if stable := validCredentialID(c.AuthID); stable != "" {
		return strings.TrimPrefix(stable, "kiro-")
	}
	if c.AuthMethod == authMethodAPIKey && c.KiroAPIKey != "" {
		sum := sha256.Sum256([]byte(c.KiroAPIKey))
		return hex.EncodeToString(sum[:10])
	}
	if c.ProfileARN != "" {
		return safeFileToken(c.ProfileARN[strings.LastIndex(c.ProfileARN, "/")+1:])
	}
	return strings.TrimPrefix(credentialID(c), "kiro-")
}

func (c credential) fileName() string {
	method := c.AuthMethod
	if c.AuthMethod == authMethodSocial {
		switch strings.ToLower(strings.TrimSpace(c.SocialProvider)) {
		case "google":
			method = "google"
		case "github":
			method = "github"
		}
	}
	switch method {
	case authMethodIDC:
		if strings.Contains(strings.ToLower(c.Label), "builder") || strings.EqualFold(c.Provider, "BuilderId") {
			method = "builderid"
		} else {
			method = "idc"
		}
	case authMethodExternalIDP:
		method = "external-idp"
	case authMethodAPIKey:
		method = "apikey"
	}
	method = safeFileToken(method)
	if method == "" {
		method = "account"
	}
	return fmt.Sprintf("kiro-%s-%s.json", method, c.fileIdentity())
}

func safeFileToken(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', strings.ContainsRune("@._+-", r):
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), ".")
}

func validateTokenEndpoint(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("External IdP token endpoint must be an https URL")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// kironxString is a local equivalent used while credential normalization runs
// before the imported field map has been copied into the typed credential.
func kironxString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
