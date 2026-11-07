package manifest

import "testing"

func TestProviderConfigurationValidatesBeforeProvisioning(t *testing.T) {
	for _, test := range []struct {
		name, credential string
		valid            bool
	}{
		{"azure_configured", `{"name":"Azure","provider":"azure","secretFromEnv":"AZURE_KEY","providerConfig":{"api_base":"https://azure.example.test","api_version":"2024-02-01"}}`, true},
		{"azure_missing", `{"name":"Azure","provider":"azure","secretFromEnv":"AZURE_KEY"}`, false},
		{"vertex_configured", `{"name":"Vertex","provider":"vertex_ai","secretFromEnv":"VERTEX_SECRET","secretKind":"service_account_json","providerConfig":{"project":"my-project","location":"us-central1"}}`, true},
		{"vertex_missing", `{"name":"Vertex","provider":"vertex_ai","secretFromEnv":"VERTEX_SECRET"}`, false},
		{"embedded_secret", `{"name":"Provider","provider":"openai","secretFromEnv":"KEY","providerConfig":{"api_key":"private-value"}}`, false},
		{"url_password", `{"name":"Provider","provider":"openai","secretFromEnv":"KEY","providerConfig":{"base_url":"https://user:password@example.test"}}`, false},
		{"url_query", `{"name":"Provider","provider":"openai","secretFromEnv":"KEY","providerConfig":{"base_url":"https://example.test?api_key=private-value"}}`, false},
		{"kind_without_input", `{"id":"provider_a","secretKind":"api_key"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := `{"version":1,"app":{"id":"app_a"},"providerCredentials":{"primary":` + test.credential + `}}`
			_, err := Parse([]byte(data))
			if (err == nil) != test.valid {
				t.Fatalf("validation: %v", err)
			}
		})
	}
}
