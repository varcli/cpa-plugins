package provider

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/varcli/cpa-plugins/plugins/kiro/kironx"
	_ "modernc.org/sqlite"
)

var sqliteTokenKeys = []string{
	"kirocli:social:token",
	"kirocli:odic:token",
	"codewhisperer:odic:token",
}

var sqliteRegistrationKeys = []string{
	"kirocli:odic:device-registration",
	"codewhisperer:odic:device-registration",
}

func importCredentials(path, mode string) ([]credential, error) {
	mode = normalizeMode(mode)
	info, errStat := os.Stat(path)
	if errStat != nil {
		return nil, fmt.Errorf("inspect Kiro credential source: %w", errStat)
	}
	if !info.IsDir() {
		creds, errLoad := importCredentialFile(path, mode)
		if errLoad != nil {
			return nil, errLoad
		}
		return deduplicateCredentials(creds), nil
	}

	var files []string
	errWalk := filepath.WalkDir(path, func(candidate string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(candidate))
		if ext == ".json" || ext == ".sqlite" || ext == ".sqlite3" || ext == ".db" {
			files = append(files, candidate)
		}
		return nil
	})
	if errWalk != nil {
		return nil, fmt.Errorf("scan Kiro credential directory: %w", errWalk)
	}
	sort.Strings(files)

	registrations := make(map[string]map[string]any)
	for _, candidate := range files {
		if strings.ToLower(filepath.Ext(candidate)) != ".json" {
			continue
		}
		raw, errRead := os.ReadFile(candidate)
		if errRead != nil {
			continue
		}
		var object map[string]any
		if json.Unmarshal(raw, &object) == nil && kironx.String(object, "clientId") != "" && kironx.String(object, "clientSecret") != "" {
			registrations[strings.TrimSuffix(filepath.Base(candidate), filepath.Ext(candidate))] = object
		}
	}

	var out []credential
	var importErrors []error
	for _, candidate := range files {
		creds, errLoad := importCredentialFile(candidate, mode)
		if errLoad != nil {
			if !errors.Is(errLoad, errUnrecognizedCredential) {
				importErrors = append(importErrors, errLoad)
			}
			continue
		}
		for index := range creds {
			if creds[index].ClientIDHash != "" && (creds[index].ClientID == "" || creds[index].ClientSecret == "") {
				if reg := registrations[creds[index].ClientIDHash]; reg != nil {
					creds[index].ClientID = kironx.String(reg, "clientId", "client_id")
					creds[index].ClientSecret = kironx.String(reg, "clientSecret", "client_secret")
				}
			}
		}
		out = append(out, creds...)
	}
	out = deduplicateCredentials(out)
	if len(out) == 0 {
		if len(importErrors) > 0 {
			return nil, fmt.Errorf("no usable Kiro credentials found: %v", importErrors[0])
		}
		return nil, fmt.Errorf("no usable Kiro credentials found in %s", path)
	}
	return out, nil
}

var errUnrecognizedCredential = errors.New("unrecognized Kiro credential source")

func importCredentialFile(path, mode string) ([]credential, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".sqlite" || ext == ".sqlite3" || ext == ".db" {
		cred, errLoad := loadSQLiteCredential(path, mode)
		if errLoad != nil {
			return nil, errLoad
		}
		return []credential{cred}, nil
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, fmt.Errorf("read Kiro credential %s: %w", path, errRead)
	}
	creds, errParse := parseCredentialJSON(raw, path, mode)
	if errParse != nil {
		return nil, errParse
	}
	for index := range creds {
		resolveSiblingRegistration(&creds[index])
	}
	return creds, nil
}

func parseCredentialJSON(raw []byte, sourcePath, mode string) ([]credential, error) {
	var value any
	if errUnmarshal := json.Unmarshal(raw, &value); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Kiro credential JSON %s: %w", sourcePath, errUnmarshal)
	}
	var objects []map[string]any
	switch typed := value.(type) {
	case map[string]any:
		if accounts, ok := typed["accounts"].([]any); ok {
			for _, item := range accounts {
				if object, okObject := item.(map[string]any); okObject {
					objects = append(objects, object)
				}
			}
		} else {
			objects = append(objects, typed)
		}
	case []any:
		for _, item := range typed {
			if object, okObject := item.(map[string]any); okObject {
				objects = append(objects, object)
			}
		}
	default:
		return nil, errUnrecognizedCredential
	}

	var out []credential
	for _, object := range objects {
		cred := credentialFromMap(object, sourcePath, "json", mode)
		if cred.RefreshToken == "" && cred.KiroAPIKey == "" {
			continue
		}
		if cred.AuthMethod == authMethodExternalIDP && cred.ClientID != "" {
			if errEndpoint := validateTokenEndpoint(cred.TokenEndpoint); errEndpoint != nil {
				return nil, errEndpoint
			}
		}
		out = append(out, cred)
	}
	if len(out) == 0 {
		return nil, errUnrecognizedCredential
	}
	return out, nil
}

func credentialFromMap(data map[string]any, sourcePath, sourceKind, mode string) credential {
	persistedSourcePath := kironx.String(data, "source_path")
	if persistedSourcePath == "" {
		persistedSourcePath = sourcePath
	}
	persistedSourceKind := kironx.String(data, "source_kind")
	if persistedSourceKind == "" {
		persistedSourceKind = sourceKind
	}
	persistedMode := kironx.String(data, "mode")
	if persistedMode == "" {
		persistedMode = mode
	}
	authMethodValue := kironx.String(data, "authMethod", "auth_method")
	authTypeValue := kironx.String(data, "authType", "auth_type")
	if authMethodValue == "" && normalizeExplicitAuthMethod(authTypeValue) != "" {
		authMethodValue = authTypeValue
	}
	cred := credential{
		Version:        1,
		AuthID:         kironx.String(data, "authId", "auth_id"),
		AuthMethod:     normalizeAuthMethod(authMethodValue, data),
		AuthType:       authTypeValue,
		Mode:           normalizeMode(persistedMode),
		SourcePath:     persistedSourcePath,
		SourceKind:     persistedSourceKind,
		SocialProvider: kironx.String(data, "social_provider"),
		Provider:       kironx.String(data, "kiroProvider", "kiro_provider", "provider"),
		SourceTokenKey: kironx.String(data, "source_token_key"),
		AccessToken:    kironx.String(data, "accessToken", "access_token"),
		RefreshToken:   kironx.String(data, "refreshToken", "refresh_token"),
		ClientID:       kironx.String(data, "clientId", "client_id"),
		ClientSecret:   kironx.String(data, "clientSecret", "client_secret"),
		ClientIDHash:   kironx.String(data, "clientIdHash", "client_id_hash"),
		TokenEndpoint:  kironx.String(data, "tokenEndpoint", "token_endpoint"),
		KiroAPIKey:     kironx.String(data, "kiroApiKey", "kiro_api_key"),
		ProfileARN:     kironx.String(data, "profileArn", "profile_arn", "arn"),
		SSORegion:      kironx.String(data, "ssoRegion", "sso_region", "region"),
		APIRegion:      kironx.String(data, "apiRegion", "api_region"),
		ExpiresAt:      expiryValue(data, "expiresAt", "expires_at", "expiresIn", "expires_in"),
		Email:          kironx.String(data, "email"),
		UserID:         kironx.String(data, "userId", "user_id"),
		Label:          kironx.String(data, "label", "name", "accountName"),
		Fingerprint:    kironx.String(data, "fingerprint"),
		DisableCooling: boolValue(data, "disable_cooling", "disableCooling"),
	}
	if scopes, okScopes := data["scopes"].([]any); okScopes {
		for _, scope := range scopes {
			if text, okText := scope.(string); okText {
				cred.Scopes = append(cred.Scopes, text)
			}
		}
	}
	finalizeCredential(&cred)
	return cred
}

func boolValue(data map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := data[key].(bool); ok {
			return value
		}
	}
	return false
}

func resolveSiblingRegistration(cred *credential) {
	if cred == nil || cred.ClientIDHash == "" || (cred.ClientID != "" && cred.ClientSecret != "") || cred.SourcePath == "" {
		return
	}
	registrationPath := filepath.Join(filepath.Dir(cred.SourcePath), cred.ClientIDHash+".json")
	raw, errRead := os.ReadFile(registrationPath)
	if errRead != nil {
		return
	}
	var registration map[string]any
	if json.Unmarshal(raw, &registration) != nil {
		return
	}
	cred.ClientID = kironx.String(registration, "clientId", "client_id")
	cred.ClientSecret = kironx.String(registration, "clientSecret", "client_secret")
	finalizeCredential(cred)
}

func loadSQLiteCredential(path, mode string) (credential, error) {
	db, errOpen := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if errOpen != nil {
		return credential{}, fmt.Errorf("open Kiro SQLite credential %s: %w", path, errOpen)
	}
	defer db.Close()

	var tokenKey, tokenJSON string
	for _, key := range sqliteTokenKeys {
		errQuery := db.QueryRow("SELECT value FROM auth_kv WHERE key = ?", key).Scan(&tokenJSON)
		if errQuery == nil {
			tokenKey = key
			break
		}
		if !errors.Is(errQuery, sql.ErrNoRows) {
			return credential{}, fmt.Errorf("read Kiro SQLite token %s: %w", path, errQuery)
		}
	}
	if tokenJSON == "" {
		return credential{}, errUnrecognizedCredential
	}
	var tokenData map[string]any
	if errJSON := json.Unmarshal([]byte(tokenJSON), &tokenData); errJSON != nil {
		return credential{}, fmt.Errorf("decode Kiro SQLite token %s: %w", path, errJSON)
	}
	cred := credentialFromMap(tokenData, path, "sqlite", mode)
	cred.SourceTokenKey = tokenKey

	for _, key := range sqliteRegistrationKeys {
		var registrationJSON string
		if db.QueryRow("SELECT value FROM auth_kv WHERE key = ?", key).Scan(&registrationJSON) != nil {
			continue
		}
		var registration map[string]any
		if json.Unmarshal([]byte(registrationJSON), &registration) == nil {
			cred.ClientID = kironx.String(registration, "clientId", "client_id")
			cred.ClientSecret = kironx.String(registration, "clientSecret", "client_secret")
			if cred.SSORegion == "" {
				cred.SSORegion = kironx.String(registration, "region")
			}
		}
		break
	}
	if cred.AuthMethod == authMethodIDC && (cred.ClientID == "" || cred.ClientSecret == "") {
		return credential{}, fmt.Errorf("Kiro IdC credentials require clientId and clientSecret")
	}
	if cred.AuthMethod == authMethodExternalIDP && cred.ClientID == "" {
		return credential{}, fmt.Errorf("Kiro External IdP credentials require clientId")
	}

	var profileJSON string
	if db.QueryRow("SELECT value FROM state WHERE key = 'api.codewhisperer.profile'").Scan(&profileJSON) == nil {
		var profile map[string]any
		if json.Unmarshal([]byte(profileJSON), &profile) == nil {
			if arn := kironx.String(profile, "arn"); arn != "" {
				cred.ProfileARN = arn
				if region := regionFromARN(arn); region != "" {
					cred.APIRegion = region
				}
			}
		}
	}
	finalizeCredential(&cred)
	if cred.RefreshToken == "" && cred.KiroAPIKey == "" {
		return credential{}, errUnrecognizedCredential
	}
	return cred, nil
}

func reloadReferencedCredential(current credential) (credential, error) {
	if current.Mode != "reference" || strings.TrimSpace(current.SourcePath) == "" {
		return current, nil
	}
	creds, errLoad := importCredentialFile(current.SourcePath, current.Mode)
	if errLoad != nil {
		return current, fmt.Errorf("reload referenced Kiro credential: %w", errLoad)
	}
	if len(creds) == 0 {
		return current, fmt.Errorf("referenced Kiro credential contains no account")
	}
	best := creds[0]
	for _, candidate := range creds {
		if current.SourceTokenKey != "" && candidate.SourceTokenKey == current.SourceTokenKey {
			best = candidate
			break
		}
		if current.ProfileARN != "" && candidate.ProfileARN == current.ProfileARN {
			best = candidate
			break
		}
	}
	if best.Label == "" {
		best.Label = current.Label
	}
	if best.Fingerprint == "" {
		best.Fingerprint = current.Fingerprint
	}
	if best.AuthID == "" {
		best.AuthID = current.AuthID
	}
	return best, nil
}

func finalizeCredential(cred *credential) {
	if cred == nil {
		return
	}
	cred.Type = providerID
	cred.Kind = "kiro"
	cred.AuthMethod = normalizeAuthMethod(cred.AuthMethod, map[string]any{
		"kiro_api_key":   cred.KiroAPIKey,
		"token_endpoint": cred.TokenEndpoint,
		"client_id":      cred.ClientID,
		"client_secret":  cred.ClientSecret,
	})
	if cred.Email == "" {
		cred.Email = cred.Label
		if !strings.Contains(cred.Email, "@") {
			cred.Email = ""
		}
	}
	if strings.EqualFold(strings.TrimSpace(cred.SocialProvider), "google") || strings.EqualFold(strings.TrimSpace(cred.SocialProvider), "github") {
		cred.SocialProvider = strings.ToLower(strings.TrimSpace(cred.SocialProvider))
	}
	if cred.SocialProvider == "" && cred.AuthMethod == authMethodSocial {
		switch strings.ToLower(strings.TrimSpace(cred.Provider)) {
		case "google":
			cred.SocialProvider = "google"
		case "github":
			cred.SocialProvider = "github"
		}
	}
	if cred.SSORegion == "" {
		cred.SSORegion = defaultRegion
	}
	if cred.APIRegion == "" {
		cred.APIRegion = regionFromARN(cred.ProfileARN)
	}
	if cred.APIRegion == "" {
		cred.APIRegion = defaultRegion
	}
	if strings.HasPrefix(cred.SourceKind, "oauth_") &&
		strings.EqualFold(cred.APIRegion, cred.SSORegion) &&
		!strings.EqualFold(cred.APIRegion, defaultRegion) &&
		strings.TrimSpace(loadedConfig().APIRegion) == "" {
		// v0.5.1 coupled the organization SSO region to the Kiro runtime
		// region. Repair those persisted OAuth credentials unless an explicit
		// api_region override says otherwise.
		cred.APIRegion = defaultRegion
	}
	switch cred.AuthMethod {
	case authMethodAPIKey:
		cred.AuthType = "kiro_api_key"
	case authMethodExternalIDP:
		cred.AuthType = "external_idp"
	case authMethodIDC:
		cred.AuthType = "aws_sso_oidc"
	default:
		cred.AuthType = "kiro_desktop"
	}
	if strings.TrimSpace(cred.Label) == "" {
		cred.Label = "Kiro " + cred.displayMethod()
	}
	if cred.Fingerprint == "" {
		sum := sha256.Sum256([]byte(cred.ProfileARN + "\x00" + cred.ClientID + "\x00" + cred.SourcePath))
		cred.Fingerprint = hex.EncodeToString(sum[:])
	}
	if cred.Provider == "" {
		switch cred.AuthMethod {
		case authMethodExternalIDP:
			cred.Provider = "AzureAD"
		case authMethodIDC:
			if strings.Contains(strings.ToLower(cred.Label), "builder") {
				cred.Provider = "BuilderId"
			} else {
				cred.Provider = "Enterprise"
			}
		case authMethodSocial:
			switch cred.SocialProvider {
			case "google":
				cred.Provider = "Google"
			case "github":
				cred.Provider = "Github"
			}
		}
	}
}

func authDataFromCredential(cred credential) (authData, error) {
	finalizeCredential(&cred)
	id := credentialID(cred)
	cred.AuthID = id
	storage, errMarshal := json.Marshal(cred)
	if errMarshal != nil {
		return authData{}, fmt.Errorf("encode Kiro credential storage: %w", errMarshal)
	}
	nextRefresh := nextRefreshTime(cred)
	label := cred.Label
	if cred.Email != "" {
		label = cred.Email + " (" + cred.displayMethod() + ")"
	} else if cred.AuthMethod == authMethodAPIKey {
		label = "Kiro API Key"
	} else if strings.TrimSpace(label) == "" {
		label = "Kiro " + cred.displayMethod()
	}
	if cred.Email != "" || cred.UserID != "" {
		id = credentialID(cred)
		cred.AuthID = id
		storage, errMarshal = json.Marshal(cred)
		if errMarshal != nil {
			return authData{}, fmt.Errorf("encode Kiro credential storage: %w", errMarshal)
		}
	}
	return authData{
		Provider:         providerID,
		ID:               id,
		FileName:         cred.fileName(),
		Label:            label,
		StorageJSON:      storage,
		Metadata:         credentialMetadata(cred),
		Attributes:       credentialAttributes(cred),
		NextRefreshAfter: nextRefresh,
	}, nil
}

// credentialMetadata / credentialAttributes expose normalized identity fields
// separately. The social identity provider is surfaced only for the providers
// the social-device flow can actually produce, so a stray persisted value
// cannot leak into the host's auth records.
func credentialMetadata(cred credential) map[string]any {
	metadata := map[string]any{"auth_type": cred.AuthType, "auth_method": cred.AuthMethod, "source_kind": cred.SourceKind}
	if cred.Email != "" {
		metadata["email"] = cred.Email
	}
	if cred.UserID != "" {
		metadata["user_id"] = cred.UserID
	}
	if provider := strings.ToLower(strings.TrimSpace(cred.SocialProvider)); provider == "google" || provider == "github" {
		metadata["social_provider"] = provider
	}
	return metadata
}

func credentialAttributes(cred credential) map[string]string {
	attributes := map[string]string{"auth_provider": providerID, "api_region": cred.APIRegion, "auth_method": cred.AuthMethod}
	if provider := strings.ToLower(strings.TrimSpace(cred.SocialProvider)); provider == "google" || provider == "github" {
		attributes["social_provider"] = provider
	}
	return attributes
}

// oauthCredentialID derives a stable per-authorization credential id. The
// identity is the social provider plus the authorization's own state, so each
// completed authorization gets its own file: signing the same Google or GitHub
// provider in again yields a separate account instead of overwriting the
// existing one.
func oauthCredentialID(socialProvider, state string) string {
	identity := "oauth\x00" + strings.TrimSpace(socialProvider) + "\x00" + strings.TrimSpace(state)
	sum := sha256.Sum256([]byte(identity))
	return "kiro-" + hex.EncodeToString(sum[:10])
}

func credentialID(cred credential) string {
	if stable := validCredentialID(cred.AuthID); stable != "" {
		if cred.Email == "" && cred.UserID == "" {
			return stable
		}
	}
	if cred.UserID != "" || cred.Email != "" {
		identity := cred.AuthMethod + "\x00" + firstNonEmpty(cred.UserID, cred.Email)
		sum := sha256.Sum256([]byte(identity))
		return "kiro-" + hex.EncodeToString(sum[:10])
	}
	identity := cred.UserID + "\x00" + cred.Email + "\x00" + cred.ProfileARN + "\x00" + cred.ClientID + "\x00" + cred.SourcePath + "\x00" + cred.SourceTokenKey
	if strings.Trim(identity, "\x00") == "" {
		identity = cred.KiroAPIKey + "\x00" + cred.RefreshToken
	}
	sum := sha256.Sum256([]byte(identity))
	return "kiro-" + hex.EncodeToString(sum[:10])
}

func validCredentialID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) != len("kiro-")+20 || !strings.HasPrefix(value, "kiro-") {
		return ""
	}
	for _, char := range value[len("kiro-"):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return ""
		}
	}
	return value
}

func nextRefreshTime(cred credential) time.Time {
	if cred.AuthMethod == authMethodAPIKey {
		return time.Now().UTC().Add(24 * time.Hour)
	}
	expires, errParse := parseExpiry(cred.ExpiresAt)
	if errParse != nil || expires.IsZero() {
		return time.Now().UTC()
	}
	refreshAt := expires.Add(-10 * time.Minute)
	if refreshAt.Before(time.Now().UTC()) {
		return time.Now().UTC()
	}
	return refreshAt
}

func parseExpiry(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("expiration is empty")
	}
	if integer, errInt := strconv.ParseInt(value, 10, 64); errInt == nil {
		if integer > 10_000_000_000 {
			integer /= 1000
		}
		return time.Unix(integer, 0).UTC(), nil
	}
	trimmed := value
	if dot := strings.Index(trimmed, "."); dot >= 0 {
		if zone := strings.IndexAny(trimmed[dot:], "Z+-"); zone >= 0 {
			absoluteZone := dot + zone
			fraction := trimmed[dot+1 : absoluteZone]
			if len(fraction) > 9 {
				trimmed = trimmed[:dot+1] + fraction[:9] + trimmed[absoluteZone:]
			}
		}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if parsed, errParse := time.Parse(layout, trimmed); errParse == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported expiration format")
}

func expiryValue(data map[string]any, keys ...string) string {
	for _, key := range keys {
		value, exists := data[key]
		if !exists || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			return typed
		case float64:
			if strings.Contains(strings.ToLower(key), "expiresin") || strings.Contains(strings.ToLower(key), "expires_in") {
				return time.Now().UTC().Add(time.Duration(typed) * time.Second).Format(time.RFC3339)
			}
			return strconv.FormatInt(int64(typed), 10)
		}
	}
	return ""
}

func regionFromARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) > 3 && strings.Count(parts[3], "-") >= 2 {
		return parts[3]
	}
	return ""
}

func normalizeMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "copy") {
		return "copy"
	}
	return "reference"
}

func deduplicateCredentials(creds []credential) []credential {
	seen := make(map[string]struct{})
	out := make([]credential, 0, len(creds))
	for _, cred := range creds {
		if cred.RefreshToken == "" && cred.KiroAPIKey == "" {
			continue
		}
		id := credentialID(cred)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, cred)
	}
	return out
}
