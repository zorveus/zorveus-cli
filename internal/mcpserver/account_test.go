package mcpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func TestAccountToolsAndImportUseTheSavedOrganizationWithoutExposingSecrets(t *testing.T) {
	var lock sync.Mutex
	requests, revocations, requestedLimit := 0, 0, 0
	status := "active"
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		lock.Lock()
		defer lock.Unlock()
		requests++
		if request.Header.Get("Authorization") != "Bearer private-access-token" {
			t.Error("MCP did not use the saved CLI credentials")
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/me":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"user":    map[string]string{"user_id": "user_a", "email": "user@example.test"},
				"session": map[string]any{"authenticated": true, "expires_at": time.Now().Add(time.Hour)},
				"orgs":    []map[string]string{{"org_id": "org_a"}}, "access_token": "private-unexpected-token",
			})
		case request.Method == http.MethodGet && request.URL.Path == "/apps":
			if request.URL.Query().Get("owner_org_id") != "org_a" {
				t.Error("app listing did not use the selected organization")
			}
			requestedLimit, _ = strconv.Atoi(request.URL.Query().Get("limit"))
			_, _ = writer.Write([]byte(`{"apps":[{"app_id":"app_a","owner_org_id":"org_a","app_name":"Assistant","status":"active","client_secret":"private-client-secret"}]}`))
		case request.Method == http.MethodGet && request.URL.Path == "/apps/app_a":
			_, _ = writer.Write([]byte(`{"app":{"app_id":"app_a","owner_org_id":"org_a","app_name":"Assistant","status":"active","allowed_models":["*"],"allowed_scopes":["inference:write"],"client_type":"confidential","product_user_charge_multiplier":"1","redirect_uris":[],"client_secret":"private-client-secret"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/app-connections":
			if request.URL.Query().Get("org_id") != "org_a" || request.URL.Query().Get("app_id") != "app_a" {
				t.Error("import did not restrict connections to the selected app and organization")
			}
			_, _ = writer.Write([]byte(`{"app_connections":[]}`))
		case request.Method == http.MethodGet && request.URL.Path == "/orgs/org_a/routing-keys":
			_, _ = writer.Write([]byte(`{"items":[]}`))
		case request.Method == http.MethodGet && request.URL.Path == "/orgs/org_a/service-keys":
			_ = json.NewEncoder(writer).Encode(map[string]any{"service_keys": []map[string]any{{
				"org_service_api_key_id": "service_a", "org_id": "org_a", "key_alias": "Billing", "status": status,
				"scopes": []string{"product_users:read"}, "token_prefix": "svc_a", "service_key": "private-service-secret",
			}}})
		case request.Method == http.MethodPost && request.URL.Path == "/orgs/org_a/service-keys/service_a/revoke":
			revocations++
			status = "revoked"
			_ = json.NewEncoder(writer).Encode(map[string]any{"revoked": true, "service_key": map[string]any{
				"org_service_api_key_id": "service_a", "org_id": "org_a", "status": status, "key_alias": "Billing", "secret": "private-service-secret",
			}})
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()
	directory := t.TempDir()
	store := &loginStore{login: auth.Login{
		APIOrigin: backend.URL, ClientID: api.ClientID, Organization: "org_a",
		AccessToken: "private-access-token", RefreshToken: "private-refresh-token", ExpiresAt: time.Now().Add(time.Hour),
		Scope: "apps:read app_connections:read routing_keys:read service_keys:read service_keys:revoke",
	}}
	session := connect(t, Options{Directory: directory, Auth: authentication(store)})
	var transcript bytes.Buffer
	invoke := func(name string, args map[string]any) *mcp.CallToolResult {
		result := call(t, session, name, args)
		_ = json.NewEncoder(&transcript).Encode(result)
		return result
	}
	identity := invoke("zorveus_whoami", map[string]any{})
	var whoami auth.WhoamiResult
	decode(t, identity, &whoami)
	if identity.IsError || whoami.User.ID != "user_a" || whoami.Organization != "org_a" || whoami.APIOrigin != backend.URL {
		t.Fatalf("whoami failed: %+v", identity)
	}
	for _, limit := range []int{1000, 17} {
		args := map[string]any{}
		if limit != 1000 {
			args["limit"] = limit
		}
		listed := invoke("zorveus_apps_list", args)
		var apps auth.AppsResult
		decode(t, listed, &apps)
		lock.Lock()
		actualLimit := requestedLimit
		lock.Unlock()
		if listed.IsError || apps.Organization != "org_a" || len(apps.Apps) != 1 || apps.Apps[0].ID != "app_a" || apps.Limit != limit || actualLimit != limit {
			t.Fatalf("app listing did not pass through its limit: %+v", listed)
		}
	}
	listed := invoke("zorveus_service_keys_list", map[string]any{})
	var keys provision.ServiceKeysResult
	decode(t, listed, &keys)
	if listed.IsError || keys.Organization != "org_a" || len(keys.Keys) != 1 || keys.Keys[0].ID != "service_a" {
		t.Fatalf("service-key list failed: %+v", listed)
	}
	lock.Lock()
	beforeRejection := requests
	lock.Unlock()
	for _, limit := range []int{0, -1, 1001} {
		requireError(t, invoke("zorveus_apps_list", map[string]any{"limit": limit}), "invalid_limit")
	}
	requireError(t, invoke("zorveus_service_keys_revoke", map[string]any{"id": "service_a", "yes": false}), "acceptance_required")
	lock.Lock()
	afterRejection := requests
	lock.Unlock()
	if beforeRejection != afterRejection {
		t.Fatal("invalid limits or unapproved revocation accessed the backend")
	}
	imported := invoke("zorveus_import", map[string]any{"app_id": "app_a", "config": "imported.json"})
	var importedApp provision.ImportResult
	decode(t, imported, &importedApp)
	config := filepath.Join(directory, "imported.json")
	if imported.IsError || importedApp.Config != config || importedApp.AppID != "app_a" {
		t.Fatalf("import failed: %+v", imported)
	}
	original, err := os.ReadFile(config)
	if err != nil || !bytes.Contains(original, []byte("app_a")) {
		t.Fatal("import did not preserve the existing app ID")
	}
	if _, err := os.Stat(filepath.Join(directory, ".zorveus", "imported.json.state.json")); err != nil {
		t.Fatal("import did not write managed state")
	}
	if !invoke("zorveus_import", map[string]any{"app_id": "app_a", "config": "imported.json"}).IsError {
		t.Fatal("import overwrote an existing manifest")
	}
	after, _ := os.ReadFile(config)
	if !bytes.Equal(original, after) {
		t.Fatal("a rejected import changed the destination")
	}
	lock.Lock()
	writesAfterImport := revocations
	lock.Unlock()
	if writesAfterImport != 0 {
		t.Fatal("inspection or import mutated the backend")
	}
	requireError(t, invoke("zorveus_service_keys_revoke", map[string]any{"id": "service_from_another_org", "yes": true}), "resource_not_found")
	for range 2 {
		revoked := invoke("zorveus_service_keys_revoke", map[string]any{"id": "service_a", "yes": true})
		var key api.ServiceKey
		decode(t, revoked, &key)
		if revoked.IsError || key.ID != "service_a" || key.Organization != "org_a" || key.Status != "revoked" {
			t.Fatalf("service-key revocation failed: %+v", revoked)
		}
	}
	lock.Lock()
	finalRevocations := revocations
	lock.Unlock()
	if finalRevocations != 1 {
		t.Fatal("revocation retried a backend write or targeted a key outside the organization")
	}
	if strings.Contains(transcript.String()+string(original), "private-") {
		t.Fatal("account tools or import exposed tokens, secrets, or unexpected API fields")
	}
}
