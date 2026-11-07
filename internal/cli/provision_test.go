package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func TestPlanCommandWritesOneJSONResultWithoutRemoteMutations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/me" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"user":    map[string]string{"user_id": "user_a", "email": "user@example.test"},
			"session": map[string]any{"authenticated": true, "expires_at": time.Now().Add(time.Hour)},
			"orgs":    []map[string]string{{"org_id": "org_a"}},
		})
	}))
	defer server.Close()
	store := &testStore{present: true, login: auth.Login{APIOrigin: server.URL, ClientID: api.ClientID, Organization: "org_a", AccessToken: "private-token", Scope: "apps:read", ExpiresAt: time.Now().Add(time.Hour)}}
	dependencies := Dependencies{Auth: &auth.Service{API: api.NewClient(), Store: store, Now: time.Now, Lock: func(context.Context) (func(), error) { return func() {}, nil }}}
	for _, test := range []struct {
		name, config, output, expected string
	}{
		{"default", "", "", "zorveus.plan.json"},
		{"development", "zorveus.dev.json", "", "zorveus.dev.plan.json"},
		{"nested production", "environments/zorveus.prod.json", "", "environments/zorveus.prod.plan.json"},
		{"spaces", "my app.json", "", "my app.plan.json"},
		{"explicit override", "zorveus.dev.json", "review.plan.json", "review.plan.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			configName := test.config
			if configName == "" {
				configName = "zorveus.json"
				t.Chdir(directory)
			}
			config := filepath.Join(directory, configName)
			if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, []byte(`{"version":1,"app":{"name":"Demo","allowedModels":["*"]}}`), 0600); err != nil {
				t.Fatal(err)
			}
			arguments := []string{"plan", "--json", "--non-interactive"}
			if test.config != "" {
				arguments = append(arguments, "--config", config)
			}
			if test.output != "" {
				arguments = append(arguments, "--out", filepath.Join(directory, test.output))
			}
			expected := test.expected
			if test.config != "" {
				expected = filepath.Join(directory, expected)
			}
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies)
			var result struct {
				OK      bool                 `json:"ok"`
				Command string               `json:"command"`
				Result  provision.PlanResult `json:"result"`
			}
			if code != 0 || json.Unmarshal(stdout.Bytes(), &result) != nil || !result.OK || result.Command != "zorveus plan" || result.Result.Plan != expected {
				t.Fatalf("plan output: %s %s", stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), "private-token") {
				t.Fatal("authorization leaked")
			}
			if _, err := os.Stat(expected); err != nil {
				t.Fatal("saved plan missing:", expected)
			}
		})
	}
}

func TestApplyRequiresExplicitAcceptanceBeforeAccessingDependencies(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"apply", "--plan", "missing.json", "--json"}, &stdout, &stderr, Dependencies{})
	var result resultEnvelope
	if code != 2 || json.Unmarshal(stdout.Bytes(), &result) != nil || result.Error == nil || result.Error.Code != "acceptance_required" {
		t.Fatalf("expected acceptance error: %s %s", stdout.String(), stderr.String())
	}
}

func TestImportStatusAndPlanFromUseOnlyReadsAndReturnOneJSONResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("unexpected mutation: %s", request.Method)
		}
		switch request.URL.Path {
		case "/me":
			_ = json.NewEncoder(writer).Encode(map[string]any{"user": map[string]string{"user_id": "user_a", "email": "user@example.test"}, "session": map[string]any{"authenticated": true, "expires_at": time.Now().Add(time.Hour)}, "orgs": []map[string]string{{"org_id": "org_a"}}})
		case "/apps/app_a":
			_, _ = writer.Write([]byte(`{"app":{"app_id":"app_a","owner_org_id":"org_a","app_name":"Demo","status":"active","allowed_models":["*"],"allowed_scopes":["inference:write"],"client_type":"confidential","product_user_charge_multiplier":"1","redirect_uris":[]}}`))
		case "/app-connections":
			_, _ = writer.Write([]byte(`{"app_connections":[]}`))
		case "/orgs/org_a/routing-keys":
			_, _ = writer.Write([]byte(`{"items":[]}`))
		default:
			t.Errorf("unexpected read %s", request.URL.Path)
		}
	}))
	defer server.Close()
	store := &testStore{present: true, login: auth.Login{APIOrigin: server.URL, ClientID: api.ClientID, Organization: "org_a", AccessToken: "private-token", Scope: "apps:read app_connections:read routing_keys:read", ExpiresAt: time.Now().Add(time.Hour)}}
	dependencies := Dependencies{Auth: &auth.Service{API: api.NewClient(), Store: store, Now: time.Now, Lock: func(context.Context) (func(), error) { return func() {}, nil }}}
	config := filepath.Join(t.TempDir(), "imported.json")
	for _, arguments := range [][]string{{"import", "--app-id", "app_a", "--config", config, "--json"}, {"status", "--config", config, "--json"}} {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies)
		var result resultEnvelope
		if code != 0 || json.Unmarshal(stdout.Bytes(), &result) != nil || !result.OK {
			t.Fatalf("command output: %s %s", stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), "private-token") {
			t.Fatal("credential leaked in command output")
		}
	}
	targetBefore, _ := os.ReadFile(config)
	source := filepath.Join(filepath.Dir(config), "dev.json")
	sourceBefore := []byte(`{"version":1,"app":{"id":"app_dev","name":"Updated"}}`)
	if err := os.WriteFile(source, sourceBefore, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	arguments := []string{"plan", "--from", source, "--config", config, "--json"}
	code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies)
	var result struct {
		OK     bool                 `json:"ok"`
		Result provision.PlanResult `json:"result"`
	}
	if code != 0 || json.Unmarshal(stdout.Bytes(), &result) != nil || !result.OK || result.Result.Operations != 1 || result.Result.Changes[0].ID != "app_a" || result.Result.Plan != filepath.Join(filepath.Dir(config), "imported.plan.json") {
		t.Fatalf("cross-manifest plan failed: %s %s", stdout.String(), stderr.String())
	}
	targetAfter, _ := os.ReadFile(config)
	sourceAfter, _ := os.ReadFile(source)
	if !bytes.Equal(targetBefore, targetAfter) || !bytes.Equal(sourceBefore, sourceAfter) {
		t.Fatal("plan --from changed an input manifest")
	}
}
