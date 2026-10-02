package provider

import (
	"encoding/json"
	"testing"
)

func restoreConfig(t *testing.T) {
	t.Helper()
	original := loadedConfig()
	t.Cleanup(func() { configValue.Store(original) })
}

func applyConfigYAML(t *testing.T, yaml string) {
	t.Helper()
	raw, errMarshal := json.Marshal(map[string][]byte{"config_yaml": []byte(yaml)})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	applyConfig(raw)
}

func TestApplyConfigParsesSocialProvider(t *testing.T) {
	restoreConfig(t)
	applyConfigYAML(t, "login_mode: social-device\nsocial_provider: github\n")
	config := loadedConfig()
	if config.LoginMode != socialDeviceLoginMode {
		t.Fatalf("login_mode = %q", config.LoginMode)
	}
	if config.SocialProvider != "github" {
		t.Fatalf("social_provider = %q, want github", config.SocialProvider)
	}
}

// Removing the key must revert it to the default rather than leaving the last
// value stuck — unlike login_mode, social_provider has no mid-flight state to
// protect.
func TestApplyConfigResetsSocialProviderWhenAbsent(t *testing.T) {
	restoreConfig(t)
	applyConfigYAML(t, "social_provider: github\n")
	if got := loadedConfig().SocialProvider; got != "github" {
		t.Fatalf("social_provider = %q, want github", got)
	}
	applyConfigYAML(t, "api_region: us-east-1\n")
	if got := loadedConfig().SocialProvider; got != defaultSocialProvider {
		t.Fatalf("social_provider = %q, want %q after the key was dropped", got, defaultSocialProvider)
	}
}

// login_mode is sticky: the host resends Register/Reconfigure with a bare or
// foreign config block during auth-store churn, and those must not yank an
// in-flight social-device login back to kiro-browser.
func TestApplyConfigKeepsLoginModeWhenAbsent(t *testing.T) {
	restoreConfig(t)
	applyConfigYAML(t, "login_mode: aws-device\n")
	if got := loadedConfig().LoginMode; got != "aws-device" {
		t.Fatalf("login_mode = %q, want aws-device", got)
	}
	applyConfigYAML(t, "api_region: us-east-1\n")
	if got := loadedConfig().LoginMode; got != "aws-device" {
		t.Fatalf("login_mode = %q, want aws-device to stick", got)
	}
}

func TestNormalizeSocialProvider(t *testing.T) {
	for raw, want := range map[string]string{
		"github": "github", "GitHub": "github", " git-hub ": "github",
		"google": "google", "Google": "google",
		"": defaultSocialProvider, "microsoft": defaultSocialProvider,
	} {
		if got := normalizeSocialProvider(raw); got != want {
			t.Fatalf("normalizeSocialProvider(%q) = %q, want %q", raw, got, want)
		}
	}
}
