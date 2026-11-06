package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestSharedReadsRejectForeignOrganizationsAndOmitSecretFields(t *testing.T) {
	for _, organization := range []string{"org_a", "org_other"} {
		t.Run(organization, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get("Authorization") != "Bearer private-token" {
					t.Error("missing bearer authorization")
				}
				if strings.Contains(request.URL.Path, "service-keys") {
					_, _ = fmt.Fprintf(writer, `{"service_keys":[{"org_service_api_key_id":"service_a","org_id":%q,"key_alias":"Backend","scopes":["product_users:read"],"status":"active","service_key":"private-secret","token_hash":"private-hash"}]}`, organization)
					return
				}
				if request.URL.Query().Get("org_id") != "org_a" {
					t.Error("missing provider organization query")
				}
				_, _ = fmt.Fprintf(writer, `{"provider_credentials":[{"provider_credential_id":"provider_a","org_id":%q,"provider":"openai","credential_name":"Primary","status":"active","routing_mode":"auto_resolve","default_model_policy":[],"active_secret_version_id":"version_a","secret":"private-secret","secret_fingerprint":"private-fingerprint","provider_config":{"api_key":"private-config-secret"}}]}`, organization)
			}))
			defer server.Close()
			client := NewClient()
			keys, keyErr := client.ListServiceKeys(context.Background(), server.URL, "org_a", "private-token")
			providers, providerErr := client.ListProviderCredentials(context.Background(), server.URL, "org_a", "private-token")
			if organization != "org_a" {
				if keyErr == nil || providerErr == nil || problem.Safe(keyErr).Code != "resource_mismatch" || problem.Safe(providerErr).Code != "resource_mismatch" {
					t.Fatal("foreign shared resources were accepted")
				}
				return
			}
			if keyErr != nil || providerErr != nil {
				t.Fatalf("valid reads failed: %v %v", keyErr, providerErr)
			}
			data, _ := json.Marshal([]any{keys, providers})
			if strings.Contains(string(data), "private-") {
				t.Fatalf("unsafe metadata exported: %s", data)
			}
		})
	}
}

func TestServiceRevocationUsesExistingEndpointAndChecksIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/orgs/org_a/service-keys/service_a/revoke" {
			t.Error("incorrect service revoke operation")
		}
		_, _ = writer.Write([]byte(`{"service_key":{"org_service_api_key_id":"service_a","org_id":"org_a","status":"revoked"},"revoked":true}`))
	}))
	defer server.Close()
	key, err := NewClient().RevokeServiceKey(context.Background(), server.URL, "org_a", "service_a", "token")
	if err != nil || key.Status != "revoked" {
		t.Fatalf("revocation not confirmed: %v", err)
	}
}

func TestServiceRotationPreservesIdentitySendsNoBodyAndHidesSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if request.Method != http.MethodPost || request.URL.Path != "/orgs/org_a/service-keys/service_a/rotate" || len(body) != 0 {
			t.Error("rotation request did not match the backend contract")
		}
		_, _ = writer.Write([]byte(`{"org_service_api_key_id":"service_a","org_id":"org_a","token_prefix":"replacement_prefix","service_key":"private-replacement-secret","scopes":["product_users:read"]}`))
	}))
	defer server.Close()
	credential, err := NewClient().RotateServiceKey(context.Background(), server.URL, "org_a", "service_a", "token")
	if err != nil || credential.Key.ID != "service_a" || credential.Secret != "private-replacement-secret" {
		t.Fatalf("rotation response was not delivered: %v", err)
	}
	encoded, _ := json.Marshal(credential)
	if strings.Contains(string(encoded), "private-replacement-secret") {
		t.Fatal("raw service credential entered ordinary JSON output")
	}
}
