package provider

import (
	"encoding/json"
	"log"
	"strings"
)

// resolveAccountIdentity fills the email and stable user id used for account
// deduplication. It is best-effort: an unavailable identity must not turn a
// successful login into a failure. The profile ARN is intentionally not
// discovered here; model listing and execution still do that on demand.
func resolveAccountIdentity(cred credential, callbackID string) credential {
	if cred.AuthMethod == authMethodAPIKey || (cred.Email != "" && cred.UserID != "") {
		return cred
	}
	if strings.TrimSpace(cred.bearerToken()) == "" {
		return cred
	}
	response, err := callKiroUsageLimits(cred, callbackID, loadedConfig().UsageURL)
	if err != nil {
		log.Printf("kiro: account identity lookup failed for auth %q: %v", cred.AuthID, err)
		return cred
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		log.Printf("kiro: account identity lookup for auth %q returned HTTP %d", cred.AuthID, response.StatusCode)
		return cred
	}
	var usage kiroUsageLimitsResponse
	if errDecode := json.Unmarshal(response.Body, &usage); errDecode != nil || usage.UserInfo == nil {
		if errDecode != nil {
			log.Printf("kiro: decode account identity for auth %q failed: %v", cred.AuthID, errDecode)
		}
		return cred
	}
	if cred.Email == "" {
		cred.Email = strings.TrimSpace(usage.UserInfo.Email)
	}
	if cred.UserID == "" {
		cred.UserID = strings.TrimSpace(usage.UserInfo.UserID)
	}
	cred = inferSocialProvider(cred)
	return cred
}

func inferSocialProvider(cred credential) credential {
	if cred.AuthMethod != authMethodSocial || cred.SocialProvider != "" {
		return cred
	}
	switch strings.ToLower(strings.TrimSpace(cred.Provider)) {
	case "google":
		cred.SocialProvider = "google"
	case "github":
		cred.SocialProvider = "github"
	}
	return cred
}

func finalizeLoginCredential(cred credential, callbackID string) credential {
	return resolveAccountIdentity(cred, callbackID)
}
