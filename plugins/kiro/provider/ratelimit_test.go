package provider

import (
	"net/http"
	"testing"
	"time"
)

func setPacingForTest(t *testing.T, minInterval, maxInterval, cooldown string) {
	t.Helper()
	original := loadedConfig()
	cfg := original
	cfg.MinTokenInterval = minInterval
	cfg.MaxTokenInterval = maxInterval
	cfg.SuspendCooldown = cooldown
	configValue.Store(cfg)
	t.Cleanup(func() {
		configValue.Store(original)
		credentialPacing.Lock()
		credentialPacing.states = make(map[string]*credentialPacingState)
		credentialPacing.Unlock()
	})
}

func TestCredentialPacingIsPerAccountAndDisableCoolingBypasses(t *testing.T) {
	setPacingForTest(t, "1ms", "1ms", "30m")
	originalNow := credentialPacing.now
	originalSleep := credentialPacing.sleep
	credentialPacing.Lock()
	credentialPacing.states = make(map[string]*credentialPacingState)
	credentialPacing.sleep = func(time.Duration) {}
	credentialPacing.Unlock()
	t.Cleanup(func() {
		credentialPacing.Lock()
		credentialPacing.now = originalNow
		credentialPacing.sleep = originalSleep
		credentialPacing.Unlock()
	})

	if err := admitCredential("a", credential{}); err != nil {
		t.Fatal(err)
	}
	if err := admitCredential("a", credential{}); err != nil {
		t.Fatalf("second same-account request failed: %v", err)
	}
	if err := admitCredential("b", credential{}); err != nil {
		t.Fatalf("other account was blocked: %v", err)
	}
	if err := admitCredential("c", credential{DisableCooling: true}); err != nil {
		t.Fatalf("disable_cooling was ignored: %v", err)
	}
}

func TestUpstreamQuotaAndSuspensionAreParked(t *testing.T) {
	setPacingForTest(t, "1ms", "1ms", "30m")
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	credentialPacing.Lock()
	credentialPacing.states = make(map[string]*credentialPacingState)
	credentialPacing.now = func() time.Time { return now }
	credentialPacing.sleep = func(time.Duration) {}
	credentialPacing.Unlock()
	t.Cleanup(func() {
		credentialPacing.Lock()
		credentialPacing.now = time.Now
		credentialPacing.sleep = time.Sleep
		credentialPacing.Unlock()
	})

	recordCredentialFailure("quota", credential{}, statusError{Code: "quota_exhausted", HTTPStatus: http.StatusPaymentRequired})
	if err := admitCredential("quota", credential{}); err == nil || err.(statusError).HTTPStatus != http.StatusPaymentRequired {
		t.Fatalf("quota parked error = %v", err)
	}

	recordCredentialFailure("suspended", credential{}, statusError{Code: "unauthorized", HTTPStatus: http.StatusForbidden, Body: `{"message":"User is not authorized to make this call"}`})
	if err := admitCredential("suspended", credential{}); err == nil || err.(statusError).HTTPStatus != http.StatusForbidden {
		t.Fatalf("suspended parked error = %v", err)
	}
}
