package manifest

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRewriteSaaSPreservesIdentitiesAndUnmanagedSettings(t *testing.T) {
	original, err := Parse([]byte(`{
  "version":1,
  "app":{"id":"app_a","name":"Old","allowedModels":["model_a","model_b"],"creditUnitsPerUsd":"100","oauth":{"clientType":"public","redirectUris":["https://example.com/callback"]}},
  "keys":{"plus":{"id":"conn_a","name":"Plus","allowedModels":["model_a"],"cap":{"amount":"10","currency":"USD","period":"monthly"}}},
  "serviceKeys":{"customers":{"id":"service_a","name":"Customer administration","scopes":["product_users:read","product_users:write"]}},
  "routingKeys":{"primary":{"id":"router_a","name":"Primary","members":["plus"],"defaultSelector":"plus"}},
  "providerCredentials":{"openai":{"id":"provider_a","name":"Existing provider","provider":"openai","routingMode":"auto_resolve","defaultModelPolicy":["model_a"]}}
}`))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := original.Bytes()
	data, err := RewriteSaaS(original, SaaSOptions{Name: "Updated", Models: []string{"model_a", "model_b"}, CreditsPerUSD: "1000", Plans: []SaaSPlan{{Reference: "plus", Name: "Premium", Allowance: "25"}, {Reference: "pro", Name: "Pro", Allowance: "30"}}})
	if err != nil {
		t.Fatal(err)
	}
	document, err := Parse(data)
	if err != nil || document.AppID() != "app_a" {
		t.Fatal("rewriting replaced the app identity")
	}
	keys := document.data["keys"].(map[string]any)
	plus := keys["plus"].(map[string]any)
	if plus["id"] != "conn_a" || plus["name"] != "Premium" || keys["pro"].(map[string]any)["id"] != nil {
		t.Fatal("rewriting confused existing and new plan identities")
	}
	models := plus["allowedModels"].([]any)
	if len(models) != 1 || models[0] != "model_a" {
		t.Fatal("rewriting broadened an existing key's model permissions")
	}
	for _, section := range []string{"serviceKeys", "routingKeys", "providerCredentials"} {
		oldValue, _ := json.Marshal(original.data[section])
		newValue, _ := json.Marshal(document.data[section])
		if !bytes.Equal(oldValue, newValue) {
			t.Fatalf("rewriting changed unmanaged %s", section)
		}
	}
	oldOAuth, _ := json.Marshal(original.data["app"].(map[string]any)["oauth"])
	newOAuth, _ := json.Marshal(document.data["app"].(map[string]any)["oauth"])
	if !bytes.Equal(oldOAuth, newOAuth) {
		t.Fatal("rewriting changed OAuth settings")
	}
	after, _ := original.Bytes()
	if !bytes.Equal(before, after) {
		t.Fatal("building a rewrite mutated the original document")
	}
}
