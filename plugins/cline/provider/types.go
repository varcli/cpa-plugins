package provider

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/varcli/cpa-plugins/plugins/cline/clinerpc"
)

// Provider identity and the endpoints the plugin talks to. Both upstream bases
// are vars rather than consts so tests can point them at httptest servers.
const (
	providerID   = "cline"
	providerName = "cline"
	pluginName   = "cline"

	// Official Cline icon, rendered by the CPA management sidebar entry.
	pluginLogoURL = "https://raw.githubusercontent.com/cline/cline/main/assets/icons/icon.png"

	// WorkOS public client used by Cline Desktop for the device grant.
	workOSClientID = "client_01K3A541FN8TA3EPPHTD2325AR"
	// workOSTokenPfx marks a Cline access token that wraps a WorkOS token.
	workOSTokenPfx = "workos:"

	clineAppBase   = "https://app.cline.bot"
	clineUserAgent = "Cline/0.0.32"
	clineVersion   = "0.0.32"

	// authFileName is the credential name CPA registers; each account is
	// persisted to its own file (see authFileNameFor).
	authFileName = "cline.json"

	loginTTL      = 10 * time.Minute
	modelCacheTTL = 5 * time.Minute

	defaultModelPrefix = "cline/"
)

var (
	clineAPIBase  = "https://api.cline.bot"
	workOSAPIBase = "https://api.workos.com"
)

// registrationPayload mirrors the host's rpcRegistration (internal/pluginhost)
// and the other provider plugins' registration output.
type registrationPayload struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelRegistrar        bool                         `json:"model_registrar"`
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
	ManagementAPI         bool                         `json:"management_api"`
}

// pluginConfig is the plugin-owned configuration decoded from the host's
// config_yaml block. Only keys declared in ConfigFields are meaningful here.
type pluginConfig struct {
	ModelPrefix       string   `json:"model_prefix"`
	EnableModelPrefix bool     `json:"enable_model_prefix"`
	HiddenModels      []string `json:"hidden_models"`
	Models            []string `json:"models"`
}

// -----------------------------------------------------------------------------
// Credentials
// -----------------------------------------------------------------------------

// storedAuth is the plugin-owned credential document. auth/account are nested
// so a physical auth file can also carry host-owned fields (label, note,
// disabled) alongside them.
type storedAuth struct {
	Auth    storedTokens  `json:"auth"`
	Account storedAccount `json:"account"`
}

type storedTokens struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"`
	TokenType    string `json:"tokenType,omitempty"`
}

type storedAccount struct {
	ID          string `json:"id"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Nickname    string `json:"nickname,omitempty"`
	Plan        string `json:"plan,omitempty"`
	PlanStatus  string `json:"planStatus,omitempty"`
}

// loginState is one in-flight WorkOS device grant, kept until the panel polls
// it to completion or it expires.
type loginState struct {
	DeviceCode   string
	UserCode     string
	Verification string
	Interval     time.Duration
	Expires      time.Time
	Provider     string
	StartedAt    time.Time
}

// -----------------------------------------------------------------------------
// Upstream wire shapes
// -----------------------------------------------------------------------------

type clineAuthResponse struct {
	Success bool          `json:"success"`
	Data    clineAuthData `json:"data"`
}

type clineAuthData struct {
	AccessToken  string        `json:"accessToken"`
	RefreshToken string        `json:"refreshToken"`
	TokenType    string        `json:"tokenType"`
	ExpiresAt    string        `json:"expiresAt"`
	UserInfo     clineUserInfo `json:"userInfo"`
}

type clineUserInfo struct {
	Subject  string `json:"subject"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	ClineUID string `json:"clineUserId"`
}

type clineMeResponse struct {
	Success bool `json:"success"`
	Data    struct {
		ID          string `json:"id"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	} `json:"data"`
}

type clinePlanResponse struct {
	Success bool `json:"success"`
	Data    *struct {
		Plan *struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
			Interval    string `json:"interval"`
			Type        string `json:"type"`
		} `json:"plan"`
		SubscriptionID   string `json:"subscriptionId"`
		CurrentPeriodEnd string `json:"currentPeriodEnd"`
	} `json:"data"`
}

type workOSDeviceResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Error                   string `json:"error"`
	ErrorDescription        string `json:"error_description"`
}

type workOSTokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// recommendedModel is one entry of Cline's recommended-models feed.
type recommendedModel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// recommendedModels is the feed, split into the four entitlement buckets Cline
// publishes. Each bucket becomes one panel group.
type recommendedModels struct {
	Recommended []recommendedModel `json:"recommended"`
	Free        []recommendedModel `json:"free"`
	ClinePass   []recommendedModel `json:"clinePass"`
	ClineCloud  []recommendedModel `json:"clineCloud"`
}

// -----------------------------------------------------------------------------
// Host RPC wire shapes
// -----------------------------------------------------------------------------

// hostAuthFileEntry is one host.auth.list row. JSON names are capitalized
// because the host marshals its own structs without tags (same convention as
// kiro/qoder).
type hostAuthFileEntry struct {
	ID        string `json:"id,omitempty"`
	AuthIndex string `json:"auth_index,omitempty"`
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Label     string `json:"label,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
	Path      string `json:"path,omitempty"`
}

type hostAuthListResponse struct {
	Files []hostAuthFileEntry `json:"files"`
}

type hostAuthGetResponse struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name,omitempty"`
	Path      string          `json:"path,omitempty"`
	JSON      json.RawMessage `json:"json"`
}

type hostHTTPRequest = clinerpc.HTTPRequest
type hostHTTPResponse = clinerpc.HTTPResponse
type hostHTTPStreamResponse = clinerpc.HTTPStreamResponse
type hostHTTPStreamReadResponse = clinerpc.HTTPStreamReadResponse

// -----------------------------------------------------------------------------
// Executor wire shapes
// -----------------------------------------------------------------------------

// executorStreamRequest adds the host-assigned stream id to the executor
// request. A non-empty StreamID means the host wants chunks pushed through
// host.stream.emit rather than returned inline.
type executorStreamRequest struct {
	pluginapi.ExecutorRequest
	StreamID string `json:"stream_id,omitempty"`
}

// streamResponse mirrors the host's rpcExecutorStreamResponse.
type streamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

// -----------------------------------------------------------------------------
// Management wire shapes
// -----------------------------------------------------------------------------

type managementRequestWire struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type managementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers,omitempty"`
	Body       []byte      `json:"Body,omitempty"`
}

// modelOverlay is the panel-editable model presentation state.
type modelOverlay struct {
	Hide  []string `json:"hide"`
	Order []string `json:"order"`
	Add   []string `json:"add"`
}

type modelOverlayState struct {
	Overlay  modelOverlay `json:"overlay"`
	Revision int64        `json:"revision"`
}

// cachedModelCatalog is one account's discovered catalog plus the metadata the
// panel reports about where it came from.
type cachedModelCatalog struct {
	Models      []pluginapi.ModelInfo
	Groups      map[string][]string
	FetchedAt   time.Time
	Source      string
	Entitlement string
}

// modelResponse answers model.static / model.for_auth.
type modelResponse struct {
	Provider   string                `json:"Provider"`
	Models     []pluginapi.ModelInfo `json:"Models"`
	AuthUpdate pluginapi.AuthData    `json:"AuthUpdate,omitempty"`
}

// authModelRequest is the model.for_auth request, including the host callback
// id needed for any upstream call made during discovery.
type authModelRequest struct {
	AuthID         string            `json:"AuthID"`
	AuthProvider   string            `json:"AuthProvider"`
	StorageJSON    []byte            `json:"StorageJSON"`
	Metadata       map[string]any    `json:"Metadata"`
	Attributes     map[string]string `json:"Attributes"`
	HostCallbackID string            `json:"host_callback_id,omitempty"`
}
