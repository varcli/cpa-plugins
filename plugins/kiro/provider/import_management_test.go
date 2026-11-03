package provider

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCredentialImportRouteSavesAPIKey(t *testing.T) {
	original := callHostCall
	t.Cleanup(func() { callHostCall = original })
	var savedName string
	var saved map[string]any
	callHostCall = func(method string, payload any) (json.RawMessage, error) {
		if method != "host.auth.save" {
			t.Fatalf("unexpected host call %q", method)
		}
		values, _ := payload.(map[string]any)
		savedName, _ = values["name"].(string)
		raw, _ := values["json"].(json.RawMessage)
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		return json.RawMessage(`{}`), nil
	}

	base := loadedManagementBasePath() + "/plugins/" + providerName
	body := `{"auth_method":"api_key","kiro_api_key":"ksk_test","label":"CI key"}`
	request := `{"Method":"POST","Path":"` + base + `/credentials/import","Body":` + string(mustJSON([]byte(body))) + `}`
	raw, errHandle := handleManagement([]byte(request))
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	status, response := decodeManagementBody(t, raw)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %v", status, response)
	}
	if !strings.HasPrefix(savedName, "kiro-apikey-") || saved["kiroApiKey"] != "ksk_test" || saved["authMethod"] != authMethodAPIKey {
		t.Fatalf("saved credential has unexpected name or auth fields")
	}
	if _, leaked := response["kiro_api_key"]; leaked {
		t.Fatalf("response contained credential field")
	}
	if _, leaked := response["kiroApiKey"]; leaked {
		t.Fatalf("response contained credential field")
	}
}
