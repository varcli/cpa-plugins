package provider

import (
	"net/http"
	"testing"
)

func TestAPIKeyRuntimeHeadersUseAPIKeyBearer(t *testing.T) {
	cred := credential{AuthMethod: authMethodAPIKey, KiroAPIKey: "ksk_runtime", APIRegion: defaultRegion}
	headers := kiroHeaders(cred)
	if got := headers.Get("Authorization"); got != "Bearer ksk_runtime" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := headers.Get("TokenType"); got != "API_KEY" {
		t.Fatalf("TokenType = %q", got)
	}
}

func TestBlockedResponseClassification(t *testing.T) {
	if !isBlockedResponse(http.StatusForbidden, []byte(`{"message":"User is not authorized to make this call."}`)) {
		t.Fatal("blocked response was not recognized")
	}
	if isBlockedResponse(http.StatusForbidden, []byte(`{"message":"The bearer token included in the request is invalid."}`)) {
		t.Fatal("invalid token was incorrectly treated as a blocked account")
	}
}
