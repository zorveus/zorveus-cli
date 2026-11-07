package manifest

import "testing"

func TestNamedManifestValidationUsesObjectNamesInPointers(t *testing.T) {
	for _, test := range []struct{ name, source, code, pointer string }{
		{"unsupported version", `{"version":2,"app":{"id":"app_a"}}`, "unsupported_version", "/version"},
		{"unknown member", `{"version":1,"app":{"name":"Demo","allowedModels":["*"]},"routingKeys":{"public":{"name":"Router","members":["missing"]}}}`, "unknown_member", "/routingKeys/public/members/0"},
		{"duplicate resource name", `{"version":1,"app":{"id":"app_a"},"keys":{"backend":{"id":"first"},"backend":{"id":"second"}}}`, "duplicate_json_key", "/keys/backend"},
		{"repeated ID", `{"version":1,"app":{"id":"app_a"},"keys":{"backend":{"id":"same"},"worker":{"id":"same"}}}`, "duplicate_id", "/keys/worker/id"},
		{"missing creation field", `{"version":1,"app":{"id":"app_a"},"keys":{"backend":{}}}`, "required_field", "/keys/backend/name"},
		{"redundant alias", `{"version":1,"app":{"id":"app_a"},"keys":{"backend":{"id":"key_a","routingAlias":"backend"}}}`, "unknown_field", "/keys/backend/routingAlias"},
		{"resource list rejected", `{"version":1,"app":{"id":"app_a"},"keys":[]}`, "invalid_type", "/keys"},
		{"null collection", `{"version":1,"app":{"id":"app_a"},"keys":null}`, "invalid_type", "/keys"},
		{"invalid decimal", `{"version":1,"app":{"id":"app_a"},"keys":{"backend":{"id":"key_a","cap":{"amount":"0","currency":"USD","period":"monthly"}}}}`, "invalid_decimal", "/keys/backend/cap/amount"},
		{"provider config", `{"version":1,"app":{"id":"app_a"},"providerCredentials":{"azure":{"name":"Azure","provider":"azure","secretFromEnv":"AZURE_KEY"}}}`, "provider_config_required", "/providerCredentials/azure/providerConfig/api_base"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.source))
			assertProblem(t, err, test.code, test.pointer)
		})
	}
}

func TestNamedManifestEmptyAndOmittedKeysRemainDifferent(t *testing.T) {
	for _, test := range []struct{ source, code string }{
		{`{"version":1,"app":{"id":"app_a"},"routingKeys":{"public":{"id":"router_a","members":["backend"]}}}`, "remote_alias_required"},
		{`{"version":1,"app":{"id":"app_a"},"keys":{},"routingKeys":{"public":{"id":"router_a","members":["backend"]}}}`, "unknown_member"},
	} {
		document, err := Parse([]byte(test.source))
		if test.code == "unknown_member" {
			assertProblem(t, err, test.code, "/routingKeys/public/members/0")
			continue
		}
		if err != nil || len(document.checks) != 1 || document.checks[0].Code != test.code {
			t.Fatalf("omitted keys did not defer lookup: %+v %v", document, err)
		}
	}
}
