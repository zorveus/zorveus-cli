package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestDestroyRequiresConfirmationForTheExactResource(t *testing.T) {
	for _, test := range []struct {
		name, kind, response string
		valid                bool
	}{
		{"app confirmed", "app", `{"app_id":"app_example","deleted":true}`, true},
		{"app not confirmed", "app", `{"app_id":"app_example","deleted":false}`, false},
		{"another app", "app", `{"app_id":"app_other","deleted":true}`, false},
		{"provider confirmed", "provider", `{"provider_credential":{"provider_credential_id":"provider_example","org_id":"org_example","status":"deleted"}}`, true},
		{"provider still active", "provider", `{"provider_credential":{"provider_credential_id":"provider_example","org_id":"org_example","status":"active"}}`, false},
		{"another organization", "provider", `{"provider_credential":{"provider_credential_id":"provider_example","org_id":"org_other","status":"deleted"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &trackedResponseBody{Reader: strings.NewReader(test.response)}
			client := &Client{HTTP: &http.Client{Transport: responseTransport{body: body}}}
			var err error
			if test.kind == "app" {
				err = client.DeleteApp(context.Background(), "https://api.example.test", "org_example", "app_example", "private-token")
			} else {
				err = client.DeleteProviderCredential(context.Background(), "https://api.example.test", "org_example", "provider_example", "private-token")
			}
			if (err == nil) != test.valid {
				t.Fatalf("unexpected deletion confirmation: %v", err)
			}
		})
	}
}
