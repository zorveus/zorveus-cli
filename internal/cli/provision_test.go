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
	directory := t.TempDir()
	config, plan := filepath.Join(directory, "zorveus.json"), filepath.Join(directory, "plan.json")
	if err := os.WriteFile(config, []byte(`{"version":1,"app":{"name":"Demo","allowedModels":["*"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"plan", "--config", config, "--out", plan, "--json", "--non-interactive"}, &stdout, &stderr, dependencies)
	var result resultEnvelope
	if code != 0 || json.Unmarshal(stdout.Bytes(), &result) != nil || !result.OK || result.Command != "zorveus plan" {
		t.Fatalf("plan output: %s %s", stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "private-token") {
		t.Fatal("authorization leaked")
	}
	if _, err := os.Stat(plan); err != nil {
		t.Fatal("saved plan missing")
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

func TestImportAndStatusCommandsUseOnlyReadsAndReturnOneJSONResult(t *testing.T) {
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
}
