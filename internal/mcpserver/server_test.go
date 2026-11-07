package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func connect(t *testing.T, options Options) *mcp.ClientSession {
	t.Helper()
	server, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func decode(t *testing.T, result *mcp.CallToolResult, output any) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil || json.Unmarshal(data, output) != nil {
		t.Fatalf("invalid structured content: %s", data)
	}
}

func requireError(t *testing.T, result *mcp.CallToolResult, code string) {
	t.Helper()
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decode(t, result, &failure)
	if !result.IsError || failure.Error.Code != code {
		t.Fatalf("expected %s, got %+v", code, result)
	}
}

func TestDiscoveryValidationAndAcceptanceWorkWithoutLogin(t *testing.T) {
	directory := t.TempDir()
	config := filepath.Join(directory, "zorveus.json")
	if err := os.WriteFile(config, []byte(`{"version":1,"app":{"name":"Assistant","allowedModels":["*"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	session := connect(t, Options{Directory: directory})
	listing, err := session.ListTools(context.Background(), nil)
	if err != nil || len(listing.Tools) != 12 {
		t.Fatalf("unexpected tools: %+v %v", listing, err)
	}
	seen := map[string]bool{}
	readOnly := map[string]bool{
		"zorveus_validate": true, "zorveus_status": true, "zorveus_whoami": true,
		"zorveus_apps_list": true, "zorveus_service_keys_list": true,
	}
	for _, tool := range listing.Tools {
		seen[tool.Name] = true
		if tool.Annotations == nil || tool.InputSchema == nil {
			t.Fatal("tool missing schema or annotations")
		}
		if readOnly[tool.Name] != tool.Annotations.ReadOnlyHint {
			t.Fatalf("incorrect read-only annotation: %s", tool.Name)
		}
	}
	for _, name := range []string{
		"zorveus_validate", "zorveus_plan", "zorveus_apply", "zorveus_status",
		"zorveus_whoami", "zorveus_apps_list", "zorveus_service_keys_list", "zorveus_service_keys_revoke",
		"zorveus_init", "zorveus_import", "zorveus_clone", "zorveus_promote",
	} {
		if !seen[name] {
			t.Fatal("missing tool:", name)
		}
	}
	validated := call(t, session, "zorveus_validate", map[string]any{})
	var result manifest.ValidationResult
	decode(t, validated, &result)
	if validated.IsError || !result.Valid || result.Config != config {
		t.Fatalf("offline validation failed: %+v", validated)
	}
	// Refusing apply must happen before reading a plan, login, or credential store.
	requireError(t, call(t, session, "zorveus_apply", map[string]any{"plan": "missing.plan.json", "yes": false}), "acceptance_required")
	requireError(t, call(t, session, "zorveus_service_keys_revoke", map[string]any{"id": "service_a", "yes": false}), "acceptance_required")
	if !call(t, session, "zorveus_service_keys_revoke", map[string]any{"id": "service_a"}).IsError {
		t.Fatal("service-key revocation accepted missing confirmation")
	}
	for _, args := range []map[string]any{{"yes": true}, {"plan": "missing.plan.json"}, {"plan": 123, "yes": true}} {
		if !call(t, session, "zorveus_apply", args).IsError {
			t.Fatal("invalid apply arguments bypassed schema validation")
		}
	}
	if !call(t, session, "zorveus_validate", map[string]any{"config": "zorveus.json", "unknown": true}).IsError {
		t.Fatal("tool accepted unknown input fields")
	}
}

type loginStore struct {
	login auth.Login
	err   error
}

func (store *loginStore) Load() (auth.Login, error) { return store.login, store.err }
func (store *loginStore) Save(login auth.Login) error {
	store.login = login
	return nil
}
func (store *loginStore) Delete() error { return nil }

func authentication(store *loginStore) *auth.Service {
	var lock sync.Mutex
	return &auth.Service{API: api.NewClient(), Store: store, Now: time.Now,
		Lock: func(context.Context) (func(), error) { lock.Lock(); return lock.Unlock, nil },
	}
}

func TestRemoteToolsReportSafeLoginErrors(t *testing.T) {
	for _, failure := range []struct {
		err  error
		code string
	}{
		{auth.ErrNoLogin, "login_required"},
		{errors.New("private-credential-store-failure"), "operation_failed"},
	} {
		t.Run(failure.code, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "zorveus.json"), []byte(`{"version":1,"app":{"id":"app_a"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			session := connect(t, Options{Directory: directory, Auth: authentication(&loginStore{err: failure.err}), Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
			for _, tool := range []struct {
				name string
				args map[string]any
			}{
				{"zorveus_plan", map[string]any{}},
				{"zorveus_status", map[string]any{}},
				{"zorveus_whoami", map[string]any{}},
				{"zorveus_apps_list", map[string]any{}},
				{"zorveus_service_keys_list", map[string]any{}},
				{"zorveus_service_keys_revoke", map[string]any{"id": "service_a", "yes": true}},
				{"zorveus_import", map[string]any{"app_id": "app_a", "config": "imported.json"}},
			} {
				result := call(t, session, tool.name, tool.args)
				requireError(t, result, failure.code)
				data, _ := json.Marshal(result)
				if strings.Contains(string(data)+logs.String(), "private-") {
					t.Fatal("an unclassified error leaked into tool output or logs")
				}
			}
		})
	}
}

type secretStore struct {
	values map[string]provision.Credential
}

func (store *secretStore) Check(string) error { return nil }
func (store *secretStore) Load(account string) (provision.Credential, error) {
	value, ok := store.values[account]
	if !ok {
		return value, provision.ErrSecretMissing
	}
	return value, nil
}
func (store *secretStore) Save(account string, value provision.Credential) error {
	store.values[account] = value
	return nil
}

func TestPlanApplyAndStatusShareProvisioningAndKeepSecretsPrivate(t *testing.T) {
	directory := t.TempDir()
	config := filepath.Join(directory, "zorveus.json")
	original := []byte(`{"version":1,"app":{"name":"Assistant","allowedModels":["*"]}}`)
	if err := os.WriteFile(config, original, 0600); err != nil {
		t.Fatal(err)
	}
	var lock sync.Mutex
	writes := 0
	app := map[string]any{"app_id": "app_a", "owner_org_id": "org_a", "app_name": "Assistant", "status": "active", "allowed_models": []string{"*"}, "client_type": "confidential", "redirect_uris": []any{}}
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		lock.Lock()
		defer lock.Unlock()
		if request.Header.Get("Authorization") != "Bearer private-access-token" {
			t.Error("tool did not reuse the CLI login")
		}
		switch {
		case request.Method == "GET" && request.URL.Path == "/me":
			_ = json.NewEncoder(writer).Encode(map[string]any{"user": map[string]string{"user_id": "user_a", "email": "user@example.test"}, "session": map[string]any{"authenticated": true, "expires_at": time.Now().Add(time.Hour)}, "orgs": []map[string]string{{"org_id": "org_a"}}})
		case request.Method == "POST" && request.URL.Path == "/apps":
			writes++
			_ = json.NewEncoder(writer).Encode(map[string]any{"app": app, "client_secret": "private-one-time-secret"})
		case request.Method == "GET" && request.URL.Path == "/apps/app_a":
			_ = json.NewEncoder(writer).Encode(map[string]any{"app": app, "client_secret": "private-response-field"})
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()
	store := &loginStore{login: auth.Login{APIOrigin: backend.URL, ClientID: api.ClientID, Organization: "org_a", AccessToken: "private-access-token", RefreshToken: "private-refresh-token", Scope: "apps:read apps:create apps:update apps:rotate_secret", ExpiresAt: time.Now().Add(time.Hour)}}
	secrets := &secretStore{values: map[string]provision.Credential{}}
	var logs bytes.Buffer
	session := connect(t, Options{Directory: directory, Auth: authentication(store), Secrets: secrets, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	var transcript bytes.Buffer
	invoke := func(name string, args map[string]any) *mcp.CallToolResult {
		result := call(t, session, name, args)
		_ = json.NewEncoder(&transcript).Encode(result)
		return result
	}
	planned := invoke("zorveus_plan", map[string]any{})
	var plan provision.PlanResult
	decode(t, planned, &plan)
	if planned.IsError || plan.Operations != 1 || plan.Plan != filepath.Join(directory, "zorveus.plan.json") {
		t.Fatalf("unexpected plan: %+v", planned)
	}
	afterPlan, _ := os.ReadFile(config)
	lock.Lock()
	writesAfterPlan := writes
	lock.Unlock()
	if !bytes.Equal(original, afterPlan) || writesAfterPlan != 0 {
		t.Fatal("planning mutated the manifest or backend")
	}
	applied := invoke("zorveus_apply", map[string]any{"plan": plan.Plan, "yes": true})
	var apply provision.ApplyResult
	decode(t, applied, &apply)
	if applied.IsError || apply.AppID != "app_a" || len(apply.CredentialAccounts) != 1 {
		t.Fatalf("apply failed: %+v", applied)
	}
	if secrets.values[apply.CredentialAccounts[0]].Secret != "private-one-time-secret" {
		t.Fatal("generated credentials did not reach the secure-store adapter")
	}
	afterApply, _ := os.ReadFile(config)
	if !bytes.Contains(afterApply, []byte("app_a")) {
		t.Fatal("apply did not write returned IDs to the manifest")
	}
	status := invoke("zorveus_status", map[string]any{})
	var current provision.StatusResult
	decode(t, status, &current)
	if status.IsError || !current.InSync || current.LastApply == nil || !current.LastApply.Complete {
		t.Fatalf("status did not reflect apply: %+v", status)
	}
	// Retrying the completed plan uses the same journal rather than creating again.
	if invoke("zorveus_apply", map[string]any{"plan": plan.Plan, "yes": true}).IsError {
		t.Fatal("retrying the saved plan failed")
	}
	lock.Lock()
	finalWrites := writes
	lock.Unlock()
	if finalWrites != 1 {
		t.Fatal("retrying a completed plan repeated creation")
	}
	// Cross-file plans must pass through both the name choice and path options.
	source := filepath.Join(directory, "dev.json")
	if err := os.WriteFile(source, []byte(`{"version":1,"app":{"id":"app_dev","name":"Development","allowedModels":["*"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	crossPlan := invoke("zorveus_plan", map[string]any{"from": "dev.json", "out": "review.plan.json", "keep_names": true})
	decode(t, crossPlan, &plan)
	if crossPlan.IsError || plan.Operations != 0 || plan.Plan != filepath.Join(directory, "review.plan.json") {
		t.Fatalf("cross-file plan did not preserve target names: %+v", crossPlan)
	}
	var saved provision.Plan
	data, err := os.ReadFile(plan.Plan)
	if err != nil || json.Unmarshal(data, &saved) != nil || !saved.KeepNames || saved.Source != source {
		t.Fatal("cross-file plan did not save its name choice or source path")
	}
	crossPlan = invoke("zorveus_plan", map[string]any{"from": "dev.json", "out": "review.plan.json"})
	decode(t, crossPlan, &plan)
	if crossPlan.IsError || plan.Operations != 1 || plan.Changes[0].Kind != "app_profile" {
		t.Fatalf("default cross-file plan did not copy the source name: %+v", crossPlan)
	}
	// Source edits after planning must still block apply before any backend write.
	if err := os.WriteFile(source, []byte(`{"version":1,"app":{"id":"app_dev","name":"Changed after planning"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	requireError(t, invoke("zorveus_apply", map[string]any{"plan": "review.plan.json", "yes": true}), "stale_plan")
	requireError(t, invoke("zorveus_plan", map[string]any{"keep_names": true}), "invalid_arguments")
	if strings.Contains(transcript.String()+logs.String()+string(afterApply), "private-") {
		t.Fatal("MCP output, logs, or manifest exposed a secret")
	}
}
