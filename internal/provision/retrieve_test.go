package provision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func setupSavedSecrets(t *testing.T) (*Service, *fakeBackend, string, string) {
	t.Helper()
	service, backend, _, config, path := setup(t)
	var document map[string]any
	_ = json.Unmarshal([]byte(newManifest), &document)
	document["serviceKeys"] = map[string]any{"customers": map[string]any{"name": "Customers", "scopes": []string{"product_users:read"}}}
	if err := os.WriteFile(config, encoded(document), 0600); err != nil {
		t.Fatal(err)
	}
	planAndApply(t, service, config, path)
	return service, backend, config, path
}

func TestRetrieveAllGeneratedSecretsByReferenceAndIDWithoutWrites(t *testing.T) {
	service, backend, config, _ := setupSavedSecrets(t)
	writes := len(backend.writes)
	for _, test := range []struct{ item, id, secret string }{
		{"app", "app_a", "private-app-secret"},
		{"keys.primary", "conn_1", "private-inference-secret"},
		{"keys.secondary", "conn_2", "private-inference-secret"},
		{"routingKeys.public", "router_1", "private-routing-secret"},
		{"serviceKeys.customers", "service_1", "private-service-secret"},
	} {
		for _, selector := range []string{test.item, test.id} {
			result, err := service.Retrieve(context.Background(), "", config, selector)
			if err != nil || result.Item != test.item || result.ID != test.id || result.Secret != test.secret {
				t.Fatalf("retrieval failed for %s: %v", selector, err)
			}
		}
	}
	if len(backend.writes) != writes {
		t.Fatal("retrieval mutated a resource")
	}
	entries, _ := os.ReadDir(directory(config))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			data, _ := os.ReadFile(filepath.Join(directory(config), entry.Name()))
			if strings.Contains(string(data), "private-") {
				t.Fatal("retrieval wrote a secret into local tracking")
			}
		}
	}
	data, _ := os.ReadFile(config)
	if strings.Contains(string(data), "private-") {
		t.Fatal("retrieval wrote a secret into the manifest")
	}
}

func TestRetrievePreservesCredentialsAfterRenameAndNoOpApply(t *testing.T) {
	service, backend, config, path := setupSavedSecrets(t)
	data, _ := os.ReadFile(config)
	data = []byte(strings.ReplaceAll(string(data), `"primary"`, `"premium"`))
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	planAndApply(t, service, config, path+".next")
	writes := len(backend.writes)
	result, err := service.Retrieve(context.Background(), "", config, "keys.premium")
	if err != nil || result.Secret != "private-inference-secret" || len(backend.writes) != writes {
		t.Fatalf("renaming lost the saved credential: %v", err)
	}
	// A copied manifest can use the same local credential store without its old filename.
	copied := filepath.Join(t.TempDir(), "zorveus.prod.json")
	if err := os.WriteFile(copied, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Retrieve(context.Background(), "", copied, "conn_1"); err != nil {
		t.Fatalf("a new manifest filename lost the saved credential: %v", err)
	}
}

func TestRetrieveRegistersExistingOperationCredentialsWithoutRotation(t *testing.T) {
	service, backend, config, _ := setupSavedSecrets(t)
	store := service.Secrets.(*memorySecrets)
	for account := range store.values {
		if strings.HasPrefix(account, "resource/") {
			delete(store.values, account)
		}
	}
	writes := len(backend.writes)
	for _, item := range []string{"app", "keys.primary", "routingKeys.public", "serviceKeys.customers"} {
		if _, err := service.Retrieve(context.Background(), "", config, item); err != nil {
			t.Fatalf("existing operation credential unavailable for %s: %v", item, err)
		}
	}
	if len(backend.writes) != writes {
		t.Fatal("registering saved credentials mutated backend resources")
	}
}

func TestRetrieveRejectsMissingStaleAndRevokedSecretsWithoutRotation(t *testing.T) {
	for _, test := range []struct {
		name, item, code string
		change           func(*Service, *fakeBackend)
	}{
		{"missing", "keys.primary", "resource_credential_missing", func(service *Service, _ *fakeBackend) {
			service.Secrets.(*memorySecrets).values = map[string]Credential{}
		}},
		{"rotated inference", "keys.primary", "resource_credential_stale", func(_ *Service, backend *fakeBackend) {
			var key Fields
			_ = json.Unmarshal(backend.connections["conn_1"]["active_key"], &key)
			key.set("api_key_id", "replacement_key")
			backend.connections["conn_1"].set("active_key", key)
		}},
		{"expired inference", "keys.primary", "resource_credential_stale", func(_ *Service, backend *fakeBackend) {
			var key Fields
			_ = json.Unmarshal(backend.connections["conn_1"]["active_key"], &key)
			key.set("expires_at", time.Now().Add(-time.Hour))
			backend.connections["conn_1"].set("active_key", key)
		}},
		{"revoked connection", "keys.primary", "resource_credential_stale", func(_ *Service, backend *fakeBackend) {
			backend.connections["conn_1"].set("status", "revoked")
		}},
		{"rotated service", "serviceKeys.customers", "resource_credential_stale", func(_ *Service, backend *fakeBackend) {
			backend.services["service_1"].set("token_prefix", "replacement-prefix")
		}},
		{"revoked service", "serviceKeys.customers", "resource_credential_stale", func(_ *Service, backend *fakeBackend) {
			backend.services["service_1"].set("status", "revoked")
		}},
		{"revoked router", "routingKeys.public", "resource_credential_stale", func(_ *Service, backend *fakeBackend) {
			backend.routers["router_1"].set("status", "revoked")
		}},
		{"public app", "app", "secret_not_available", func(_ *Service, backend *fakeBackend) {
			backend.app.set("client_type", "public")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, backend, config, _ := setupSavedSecrets(t)
			test.change(service, backend)
			writes := len(backend.writes)
			result, err := service.Retrieve(context.Background(), "", config, test.item)
			if err == nil || problem.Safe(err).Code != test.code || result.Secret != "" || len(backend.writes) != writes {
				t.Fatalf("unexpected retrieval failure: %v, writes=%v", err, backend.writes)
			}
		})
	}
}

func TestRetrieveRejectsUnappliedAndUnknownItemsBeforeNetwork(t *testing.T) {
	service, backend, _, config, _ := setup(t)
	for _, test := range []struct{ item, code string }{
		{"app", "apply_required"},
		{"keys.primary", "apply_required"},
		{"keys.unknown", "item_not_found"},
		{"foreign_id", "item_not_found"},
	} {
		_, err := service.Retrieve(context.Background(), "", config, test.item)
		if problem.Safe(err).Code != test.code {
			t.Fatalf("%s returned %v", test.item, err)
		}
	}
	if len(backend.reads) != 0 || len(backend.writes) != 0 {
		t.Fatal("an invalid selection contacted the backend")
	}
}

func TestRetrieveDoesNotUseAnotherUsersSavedOperationCredentials(t *testing.T) {
	service, backend, config, _ := setupSavedSecrets(t)
	store := service.Secrets.(*memorySecrets)
	for account := range store.values {
		if strings.HasPrefix(account, "resource/") {
			delete(store.values, account)
		}
	}
	service.Auth.API.HTTP.Transport = recoveryTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/me" {
			return backend.RoundTrip(request)
		}
		writer := httptest.NewRecorder()
		_, _ = writer.WriteString(`{"user":{"user_id":"another_user","email":"other@example.test"},"session":{"authenticated":true,"expires_at":"2099-01-01T00:00:00Z"},"orgs":[{"org_id":"org_a"}]}`)
		return writer.Result(), nil
	})
	_, err := service.Retrieve(context.Background(), "", config, "keys.primary")
	if problem.Safe(err).Code != "credential_target_mismatch" {
		t.Fatalf("another user's operation credentials were reused: %v", err)
	}
}

func TestResourceAccountsSeparateTargets(t *testing.T) {
	target := Target{Origin: "https://api.example.test", Org: "org_a", User: "user_a"}
	account := credentialAccount(target, "key", "conn_1")
	for _, other := range []Target{
		{Origin: "http://localhost:8000", Org: target.Org, User: target.User},
		{Origin: target.Origin, Org: "org_b", User: target.User},
		{Origin: target.Origin, Org: target.Org, User: "user_b"},
	} {
		if credentialAccount(other, "key", "conn_1") == account {
			t.Fatal("different API, organization, or user shares a credential account")
		}
	}
}

func TestStableCredentialSaveFailureResumesWithoutRotation(t *testing.T) {
	service, backend, _, config, path := setup(t)
	store := service.Secrets.(*memorySecrets)
	store.failAccountPrefix = "resource/"
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), "", path, true, false); err == nil {
		t.Fatal("stable save failure was ignored")
	}
	writes := len(backend.writes)
	store.failAccountPrefix = ""
	if _, err := service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatal(err)
	}
	if len(backend.writes) != writes {
		t.Fatal("registering saved credentials repeated creation or rotation")
	}
}
