package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

type wizardSecrets map[string]provision.Credential

func (store wizardSecrets) Check(string) error { return nil }
func (store wizardSecrets) Load(account string) (provision.Credential, error) {
	credential, exists := store[account]
	if !exists {
		return credential, provision.ErrSecretMissing
	}
	return credential, nil
}
func (store wizardSecrets) Save(account string, credential provision.Credential) error {
	store[account] = credential
	return nil
}

func TestSetupAppliesThenRetrievesSecretsWithoutMoreWrites(t *testing.T) {
	dependencies := wizardDependencies(t)
	dependencies.Secrets = wizardSecrets{}
	identity := dependencies.Auth.API.HTTP.Transport
	var app, connection, cap, inference, serviceKey map[string]any
	writes := 0
	dependencies.Auth.API.HTTP.Transport = requestHandler(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/me" {
			return identity.RoundTrip(request)
		}
		if request.Header.Get("Authorization") != "Bearer private-token" {
			t.Fatal("management access was not authenticated")
		}
		writer := httptest.NewRecorder()
		var payload map[string]any
		if request.Body != nil {
			_ = json.NewDecoder(request.Body).Decode(&payload)
		}
		if request.Method != http.MethodGet {
			writes++
		}
		var result any
		switch request.Method + " " + request.URL.Path {
		case "POST /apps":
			app = payload
			app["app_id"], app["status"] = "app_a", "active"
			app["product_user_charge_multiplier"] = "1"
			app["allowed_scopes"] = []string{"inference:write"}
			result = map[string]any{"app": app, "client_secret": "private-app"}
		case "GET /apps/app_a":
			result = map[string]any{"app": app}
		case "POST /app-connections/dashboard":
			connection = map[string]any{"app_connection_id": "conn_a", "app_id": "app_a", "selected_org_id": "org_a", "connection_type": "org_programmatic", "status": "active", "credit_mode": payload["credit_mode"], "overrun_policy": payload["overrun_policy"], "approved_scopes": payload["approved_scopes"], "allowed_models": payload["allowed_models"]}
			inference = map[string]any{"api_key_id": "key_a", "app_connection_id": "conn_a", "status": "active", "key_alias": payload["key_alias"], "allowed_models": payload["allowed_models"]}
			connection["active_key"] = inference
			result = map[string]any{"app_connection": connection, "api_key_id": "key_a", "api_key": "private-inference"}
		case "GET /app-connections/conn_a":
			result = map[string]any{"app_connection": connection, "cap_rule": cap}
		case "GET /app-connections/conn_a/keys":
			result = map[string]any{"app_connection": connection, "api_keys": []any{inference}}
		case "PATCH /app-connections/conn_a/keys/key_a":
			for name, value := range payload {
				inference[name] = value
			}
			result = map[string]any{}
		case "PATCH /app-connections/conn_a/cap":
			cap = payload
			cap["cap_rule_id"], cap["org_id"], cap["target_type"], cap["target_id"], cap["status"] = "cap_a", "org_a", "app_connection", "conn_a", "active"
			result = map[string]any{}
		case "POST /orgs/org_a/service-keys":
			serviceKey = payload
			serviceKey["org_service_api_key_id"], serviceKey["org_id"], serviceKey["status"], serviceKey["token_prefix"] = "service_a", "org_a", "active", "prefix-a"
			response := maps.Clone(serviceKey)
			response["service_key"] = "private-service"
			result = response
		case "GET /orgs/org_a/service-keys":
			result = map[string]any{"service_keys": []any{serviceKey}}
		default:
			t.Fatalf("unexpected setup request: %s %s", request.Method, request.URL.Path)
		}
		_ = json.NewEncoder(writer).Encode(result)
		return writer.Result(), nil
	})
	config := filepath.Join(t.TempDir(), "zorveus.existing.json")
	dependencies.Stdin = strings.NewReader("\n\n\n\nn\ny\ny\n")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"setup", "--name", "Demo", "--model", "model_a", "--config", config}, &stdout, &stderr, dependencies)
	if code != 0 || writes != 4 {
		t.Fatalf("setup failed: code=%d writes=%d %s %s", code, writes, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "private-") {
		t.Fatal("setup output exposed a credential")
	}
	for _, question := range []string{"Launch app", "Executable", "Argument"} {
		if strings.Contains(stdout.String(), question) {
			t.Fatalf("setup asked an unrelated application question: %s", question)
		}
	}
	for _, item := range []struct{ selector, secret string }{
		{"app", "private-app"},
		{"keys.plus", "private-inference"},
		{"conn_a", "private-inference"},
		{"serviceKeys.customers", "private-service"},
		{"service_a", "private-service"},
	} {
		stdout.Reset()
		stderr.Reset()
		code = Execute(context.Background(), []string{"retrieve", item.selector, "--config", config}, &stdout, &stderr, dependencies)
		if code != 0 || writes != 4 || stdout.String() != item.secret+"\n" || stderr.Len() != 0 {
			t.Fatalf("retrieval failed for %s: code=%d writes=%d diagnostics=%s", item.selector, code, writes, stderr.String())
		}
	}
	stdout.Reset()
	code = Execute(context.Background(), []string{"retrieve", "keys.plus", "--config", config, "--json"}, &stdout, &stderr, dependencies)
	var retrieved struct {
		OK     bool                      `json:"ok"`
		Result provision.RetrievedSecret `json:"result"`
	}
	if code != 0 || json.Unmarshal(stdout.Bytes(), &retrieved) != nil || !retrieved.OK || retrieved.Result.Item != "keys.plus" || retrieved.Result.ID != "conn_a" || retrieved.Result.Secret != "private-inference" || writes != 4 || stderr.Len() != 0 {
		t.Fatal("explicit JSON retrieval did not return the selected secret")
	}
	dependencies.Stdin = strings.NewReader("")
	stdout.Reset()
	code = Execute(context.Background(), []string{"setup", "--non-interactive", "--yes", "--config", config}, &stdout, &stderr, dependencies)
	if code != 0 || writes != 4 || !strings.Contains(stdout.String(), "Setup complete") || !strings.Contains(stdout.String(), "No changes") || stderr.Len() != 0 {
		t.Fatalf("wizard rerun failed: %d, writes=%d %s %s", code, writes, stdout.String(), stderr.String())
	}
	// A stale default file must reuse this applied app, rather than clone its
	// settings into another file and create duplicate resources.
	defaultConfig := filepath.Join(filepath.Dir(config), "zorveus.json")
	if _, err := manifest.InitSaaS(defaultConfig, manifest.SaaSOptions{Name: "Unapplied answers", CreditsPerUSD: "1000", Plans: []manifest.SaaSPlan{{Reference: "plus", Name: "Plus", Allowance: "1000"}}}); err != nil {
		t.Fatal(err)
	}
	statePath, stateBefore := savedSetupState(t, defaultConfig)
	answersBefore, _ := os.ReadFile(defaultConfig)
	dependencies.Stdin = strings.NewReader("\n")
	stdout.Reset()
	stderr.Reset()
	code = Execute(context.Background(), []string{"setup", "--config", defaultConfig}, &stdout, &stderr, dependencies)
	if code != 0 || writes != 4 || !strings.Contains(stdout.String(), "Saved setups") || !strings.Contains(stdout.String(), "No changes") || strings.Contains(stdout.String(), "Create these resources") || strings.Contains(stdout.String(), "Apply these changes") || strings.Contains(stdout.String(), "New manifest") {
		t.Fatalf("existing setup was not reused: %d, writes=%d %s %s", code, writes, stdout.String(), stderr.String())
	}
	var restoredState provision.State
	stateAfter, _ := os.ReadFile(statePath)
	if json.Unmarshal(stateAfter, &restoredState) != nil || restoredState.AppID != "app_a" {
		t.Fatal("default file did not regain the applied app's tracking")
	}
	restored, err := manifest.Load(defaultConfig)
	if err != nil || restored.AppID() != "app_a" {
		t.Fatal("default file did not regain the applied resource IDs")
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(config), ".zorveus", "setup-backup-*"))
	if len(backups) != 1 {
		t.Fatal("previous default files were not backed up")
	}
	oldAnswers, _ := os.ReadFile(filepath.Join(backups[0], filepath.Base(defaultConfig)))
	oldState, _ := os.ReadFile(filepath.Join(backups[0], filepath.Base(statePath)))
	if !bytes.Equal(oldAnswers, answersBefore) || !bytes.Equal(oldState, stateBefore) {
		t.Fatal("recovery did not preserve the previous default files exactly")
	}
	stdout.Reset()
	stderr.Reset()
	code = Execute(context.Background(), []string{"retrieve", "keys.plus", "--config", defaultConfig}, &stdout, &stderr, dependencies)
	if code != 0 || writes != 4 || stdout.String() != "private-inference\n" || stderr.Len() != 0 {
		t.Fatalf("restored setup lost its saved secret: code=%d diagnostics=%s", code, stderr.String())
	}
	dependencies.Stdin = strings.NewReader("n\ny\n\n\n\nPremium\n30\nn\nn\ny\ny\n")
	stdout.Reset()
	stderr.Reset()
	code = Execute(context.Background(), []string{"setup", "--config", defaultConfig}, &stdout, &stderr, dependencies)
	if code != 0 || writes != 6 || !strings.Contains(stdout.String(), "Rewrite this configuration?") || !strings.Contains(stdout.String(), "Setup complete") {
		t.Fatalf("applied rewrite failed: %d, writes=%d %s %s", code, writes, stdout.String(), stderr.String())
	}
	rewritten, err := manifest.Load(defaultConfig)
	if err != nil || rewritten.AppID() != "app_a" {
		t.Fatal("rewriting replaced the applied app")
	}
	key := rewritten.Resources("keys")[0]
	if key.Name != "plus" || key.Fields["id"] != "conn_a" || key.Fields["name"] != "Premium" || key.Fields["cap"].(map[string]any)["amount"] != "30" {
		t.Fatal("rewriting replaced the plan identity instead of updating it")
	}
}
