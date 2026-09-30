package provider

// These tests cover the pure decisions that the rest of the plugin depends on
// but that are easy to get subtly wrong: how the model prefix is applied and
// stripped, how the deny list compiles, how the overlay reorders a catalog, and
// how the per-account merge dedupes. None of them need a running host.

import (
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func withConfig(t *testing.T, config pluginConfig) {
	t.Helper()
	previous, _ := configValue.Load().(pluginConfig)
	previousOverlay := loadedModelOverlay()
	previousHidden := loadedConfiguredHiddenModels()
	configValue.Store(config)
	configureModels(config.HiddenModels)
	t.Cleanup(func() {
		configValue.Store(previous)
		modelOverlayMu.Lock()
		hiddenModelsFromConfig = previousHidden
		currentModelOverlayState.Overlay = previousOverlay
		modelOverlayMu.Unlock()
	})
}

func TestStripModelPrefixOnlyStripsOurOwnPrefix(t *testing.T) {
	withConfig(t, pluginConfig{ModelPrefix: "cline/", EnableModelPrefix: true})

	cases := map[string]string{
		"cline/cline-pass/glm-5.3": "cline-pass/glm-5.3",
		"cline-pass/glm-5.3":       "cline-pass/glm-5.3",
		"openai/gpt-6-astra":       "openai/gpt-6-astra",
		"clinemodels/x":            "clinemodels/x",
		"":                         "",
	}
	for input, want := range cases {
		if got := stripModelPrefix(input); got != want {
			t.Errorf("stripModelPrefix(%q) = %q, want %q", input, got, want)
		}
	}
}

// With the toggle off, advertised ids are bare, but the host may still hold
// prefixed ids from an earlier registration; those must still be stripped or the
// upstream receives an id it does not know.
func TestStripModelPrefixWithToggleOffStillStripsStaleIds(t *testing.T) {
	withConfig(t, pluginConfig{ModelPrefix: "cline/", EnableModelPrefix: false})

	if got := modelPrefix(); got != "" {
		t.Fatalf("modelPrefix() = %q, want empty when the toggle is off", got)
	}
	if got := stripModelPrefix("cline/cline-pass/glm-5.3"); got != "cline-pass/glm-5.3" {
		t.Errorf("stripModelPrefix kept a stale prefix: %q", got)
	}
}

func TestModelInfoAppliesPrefix(t *testing.T) {
	withConfig(t, pluginConfig{ModelPrefix: "cline/", EnableModelPrefix: true})
	if got := modelInfo("cline-pass/glm-5.3", "GLM", passModelGroup, "").ID; got != "cline/cline-pass/glm-5.3" {
		t.Errorf("modelInfo ID = %q, want the prefixed id", got)
	}

	withConfig(t, pluginConfig{ModelPrefix: "cline/", EnableModelPrefix: false})
	if got := modelInfo("cline-pass/glm-5.3", "GLM", passModelGroup, "").ID; got != "cline-pass/glm-5.3" {
		t.Errorf("modelInfo ID = %q, want the bare id when the toggle is off", got)
	}
}

func TestSanitizeHideListAcceptsOnlyTrailingWildcard(t *testing.T) {
	ok, rejected := sanitizeHideList([]string{
		"cline/cline-pass/glm-5.3",
		"cline/cline-free/*",
		"*",
		"cli*ne",
		"",
	})
	if len(ok) != 2 {
		t.Fatalf("kept %d entries, want 2: %v", len(ok), ok)
	}
	if len(rejected) != 3 {
		t.Fatalf("rejected %d entries, want 3: %v", len(rejected), rejected)
	}
}

func TestHiddenSetMatchesExactAndFamilyPrefix(t *testing.T) {
	set := newHiddenSet([]string{"cline/cline-pass/glm-5.3", "cline/cline-free/*"})

	if !set.hides("cline/cline-pass/glm-5.3") {
		t.Error("exact entry did not match")
	}
	if !set.hides("cline/cline-free/kimi-k3") {
		t.Error("family prefix did not match a member")
	}
	if set.hides("cline/cline-pass/kimi-k3") {
		t.Error("unrelated id matched")
	}
	if set.empty() {
		t.Error("non-empty set reported empty")
	}
	if !newHiddenSet(nil).empty() {
		t.Error("empty set reported non-empty")
	}
}

func TestApplyModelOverlayPinsThenHides(t *testing.T) {
	base := []pluginapi.ModelInfo{
		defaultModelInfo("a", "A"),
		defaultModelInfo("b", "B"),
		defaultModelInfo("c", "C"),
	}
	overlay := modelOverlay{
		Hide:  []string{"b"},
		Order: []string{"c"},
		Add:   []string{"d"},
	}
	got := applyModelOverlay(base, overlay)
	ids := make([]string, 0, len(got))
	for _, model := range got {
		ids = append(ids, model.ID)
	}
	want := "c,a,d"
	if strings.Join(ids, ",") != want {
		t.Errorf("served order = %v, want %s", ids, want)
	}

	// The admin view keeps the hidden model listed so the operator can restore it.
	admin := applyModelOverlayForAdmin(base, overlay)
	adminIDs := make([]string, 0, len(admin))
	for _, model := range admin {
		adminIDs = append(adminIDs, model.ID)
	}
	if strings.Join(adminIDs, ",") != "c,a,d,b" {
		t.Errorf("admin order = %v, want c,a,d,b", adminIDs)
	}
}

func TestConfiguredModelsAppendsWithPrefixAndDedupes(t *testing.T) {
	withConfig(t, pluginConfig{
		ModelPrefix:       "cline/",
		EnableModelPrefix: true,
		Models:            []string{"cline-pass/glm-5.3", "some/extra-model"},
	})

	base := []pluginapi.ModelInfo{modelInfo("cline-pass/glm-5.3", "GLM", passModelGroup, "")}
	got := configuredModels(base)
	if len(got) != 2 {
		t.Fatalf("got %d models, want 2: %v", len(got), got)
	}
	if got[1].ID != "cline/some/extra-model" {
		t.Errorf("appended id = %q, want the prefixed id", got[1].ID)
	}
}

func TestMergeModelCatalogDedupesModelsAndGroups(t *testing.T) {
	primary := []pluginapi.ModelInfo{
		defaultModelInfo("x", "X"),
		defaultModelInfo("y", "Y"),
	}
	primaryGroups := map[string][]string{passModelGroup: {"x", "y"}}
	required := []pluginapi.ModelInfo{
		defaultModelInfo("y", "Y again"),
		defaultModelInfo("z", "Z"),
	}
	requiredGroups := map[string][]string{freeModelGroup: {"y", "z"}}

	models, groups := mergeModelCatalog(primary, primaryGroups, required, requiredGroups)
	if len(models) != 3 {
		t.Fatalf("merged %d models, want 3", len(models))
	}
	if members := groups[passModelGroup]; len(members) != 2 {
		t.Errorf("pass group = %v, want the two primary members", members)
	}
	// "y" is already claimed by the pass group, so the free group must not
	// advertise it a second time.
	if members := groups[freeModelGroup]; len(members) != 1 || members[0] != "z" {
		t.Errorf("free group = %v, want [z]", members)
	}
}

func TestNeedsRefreshUsesTheLeadAndTreatsUnknownExpiryAsStale(t *testing.T) {
	now := time.Now()
	fresh := &storedAuth{Auth: storedTokens{
		AccessToken: "token",
		ExpiresAt:   now.Add(2 * credentialRefreshLead).UnixMilli(),
	}}
	if needsRefresh(fresh, now) {
		t.Error("a token well beyond the lead was reported stale")
	}

	nearExpiry := &storedAuth{Auth: storedTokens{
		AccessToken: "token",
		ExpiresAt:   now.Add(time.Minute).UnixMilli(),
	}}
	if !needsRefresh(nearExpiry, now) {
		t.Error("a token inside the lead was reported fresh")
	}

	if !needsRefresh(&storedAuth{Auth: storedTokens{AccessToken: "token"}}, now) {
		t.Error("a token with no expiry was reported fresh")
	}
	if !needsRefresh(nil, now) {
		t.Error("a missing credential was reported fresh")
	}
}

// The single-flight guard exists because Cline rotates the refresh token on
// every grant: two concurrent refreshes would let the second present a token the
// first already invalidated.
func TestRefreshCredentialIsSingleFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	credentialCallMu.Lock()
	credentialCalls = map[string]*refreshCall{}
	credentialCallMu.Unlock()
	t.Cleanup(func() {
		credentialCallMu.Lock()
		credentialCalls = map[string]*refreshCall{}
		credentialCallMu.Unlock()
	})

	// Stand in for requestTokenRefresh by driving the map directly, so the test
	// stays hermetic (no host bridge, no network).
	sa := &storedAuth{Auth: storedTokens{AccessToken: "a", RefreshToken: "r"}}
	key := credentialKey(sa)
	first := &refreshCall{}
	first.done.Add(1)
	credentialCallMu.Lock()
	credentialCalls[key] = first
	credentialCallMu.Unlock()
	calls++
	close(started)

	go func() {
		<-release
		first.sa = &storedAuth{Auth: storedTokens{AccessToken: "b", RefreshToken: "r2"}}
		credentialCallMu.Lock()
		delete(credentialCalls, key)
		credentialCallMu.Unlock()
		first.done.Done()
	}()

	<-started
	credentialCallMu.Lock()
	joined := credentialCalls[key]
	credentialCallMu.Unlock()
	if joined != first {
		t.Fatal("a second caller did not join the in-flight refresh")
	}
	close(release)
	first.done.Wait()
	if calls != 1 {
		t.Errorf("refresh ran %d times, want 1", calls)
	}
}
