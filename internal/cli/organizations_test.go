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

	"github.com/zalando/go-keyring"
	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func savedOrganization(organization string) auth.Login {
	return auth.Login{APIOrigin: "http://localhost:8000", DashboardOrigin: "http://localhost:3000", ClientID: api.ClientID,
		Organization: organization, AccessToken: organization + "-access", RefreshToken: organization + "-refresh",
		Scope: defaultLoginScopes, ExpiresAt: time.Now().Add(time.Hour)}
}

func organizationDependencies(t *testing.T, logins ...auth.Login) Dependencies {
	t.Helper()
	keyring.MockInit()
	store := auth.CredentialStore{Directory: t.TempDir()}
	if err := store.Write(auth.SavedLogins{Version: 1, Logins: logins}); err != nil {
		t.Fatal(err)
	}
	dependencies := testDependencies(&testStore{})
	dependencies.Auth.Store = store
	return dependencies
}

func TestLoginAsksForDefaultAndKeepsOtherOrganizationSessions(t *testing.T) {
	dependencies := organizationDependencies(t)
	dependencies.InputIsTerminal = true
	selected := "org_a"
	revoked := []string{}
	dependencies.OpenBrowser = func(_ context.Context, address string) error {
		approveRecoveryLogin(t, address)
		return nil
	}
	dependencies.Auth.API.HTTP.Transport = requestHandler(func(request *http.Request) (*http.Response, error) {
		writer := httptest.NewRecorder()
		if request.URL.Path == "/oauth/management/revoke" {
			_ = request.ParseForm()
			revoked = append(revoked, request.Form.Get("token"))
		} else {
			_ = json.NewEncoder(writer).Encode(api.Tokens{AccessToken: selected + "-access", RefreshToken: selected + "-refresh",
				OrgID: selected, Scope: defaultLoginScopes, TokenType: "Bearer", ExpiresIn: 300})
		}
		return writer.Result(), nil
	})
	for _, test := range []struct{ organization, answer, wantDefault string }{
		{"org_a", "n\n", ""},
		{"org_b", "y\n", "org_b"},
		{"org_a", "n\n", "org_b"},
	} {
		selected = test.organization
		dependencies.Stdin = strings.NewReader(test.answer)
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"login", "--local", "--org", selected, "--json"}, &stdout, &stderr, dependencies)
		if code != 0 || !strings.Contains(stderr.String(), "Use this organization as your default?") {
			t.Fatalf("login/default prompt failed: %d %s %s", code, &stdout, &stderr)
		}
		saved, err := dependencies.Auth.Store.Read()
		if err != nil || saved.Default.Organization != test.wantDefault {
			t.Fatalf("default was not preserved or selected: %v", err)
		}
	}
	saved, _ := dependencies.Auth.Store.Read()
	if len(saved.Logins) != 2 || len(revoked) != 1 || revoked[0] != "org_a-refresh" {
		t.Fatalf("login affected other organizations: entries=%d revoked=%v", len(saved.Logins), revoked)
	}
}

func TestCommandsUseExplicitOrganizationAndDefaultOrAskWhenNeitherApplies(t *testing.T) {
	a, b := savedOrganization("org_a"), savedOrganization("org_b")
	dependencies := organizationDependencies(t, a, b)
	used := ""
	dependencies.Auth.API.HTTP.Transport = requestHandler(func(request *http.Request) (*http.Response, error) {
		used = strings.TrimSuffix(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "), "-access")
		writer := httptest.NewRecorder()
		if request.URL.Path == "/apps" {
			if request.URL.Query().Get("owner_org_id") != used {
				t.Fatal("organization and token did not match")
			}
			_, _ = writer.WriteString(`{"apps":[]}`)
		} else {
			_, _ = writer.WriteString(`{"user":{"user_id":"user_a","email":"user@example.test"},"session":{"authenticated":true,"expires_at":"2099-01-01T00:00:00Z"}}`)
		}
		return writer.Result(), nil
	})
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"whoami", "--non-interactive", "--json"}, &stdout, &stderr, dependencies)
	if code != 3 || !strings.Contains(stdout.String(), "organization_required") || used != "" {
		t.Fatalf("headless command guessed an organization: %d %s", code, &stdout)
	}
	dependencies.InputIsTerminal = true
	dependencies.Stdin = strings.NewReader("2\n")
	stdout.Reset()
	stderr.Reset()
	if code := Execute(context.Background(), []string{"whoami"}, &stdout, &stderr, dependencies); code != 0 || used != "org_b" {
		t.Fatalf("interactive organization choice failed: %d %s %s", code, &stdout, &stderr)
	}
	if err := dependencies.Auth.SetDefault(context.Background(), auth.LoginReference{APIOrigin: a.APIOrigin, Organization: a.Organization}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		arguments []string
		want      string
	}{
		{[]string{"apps", "list", "--non-interactive"}, "org_a"},
		{[]string{"apps", "list", "--org", "org_b", "--non-interactive"}, "org_b"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := Execute(context.Background(), test.arguments, &stdout, &stderr, dependencies); code != 0 || used != test.want {
			t.Fatalf("organization selection failed: %d %s %s", code, &stdout, &stderr)
		}
	}
}

func TestPlanUsesManifestOrganizationAndRejectsConflictingFlagBeforeRequests(t *testing.T) {
	a, b := savedOrganization("org_a"), savedOrganization("org_b")
	dependencies := organizationDependencies(t, a, b)
	if err := dependencies.Auth.SetDefault(context.Background(), auth.LoginReference{APIOrigin: a.APIOrigin, Organization: a.Organization}); err != nil {
		t.Fatal(err)
	}
	requests := 0
	dependencies.Auth.API.HTTP.Transport = requestHandler(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("Authorization") != "Bearer org_b-access" || request.URL.Path != "/me" {
			t.Fatalf("plan used the wrong organization: %s %s", request.Method, request.URL.Path)
		}
		writer := httptest.NewRecorder()
		_, _ = writer.WriteString(`{"user":{"user_id":"user_b","email":"user@example.test"},"session":{"authenticated":true,"expires_at":"2099-01-01T00:00:00Z"}}`)
		return writer.Result(), nil
	})
	directory := t.TempDir()
	config := filepath.Join(directory, "zorveus.json")
	original := []byte(`{"version":1,"organizationId":"org_b","app":{"name":"Project B","allowedModels":["*"]}}`)
	if err := os.WriteFile(config, original, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	arguments := []string{"plan", "--config", config, "--non-interactive", "--json"}
	if code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("manifest organization selection failed: %d %s %s", code, &stdout, &stderr)
	}
	data, _ := os.ReadFile(filepath.Join(directory, "zorveus.plan.json"))
	var plan provision.Plan
	if json.Unmarshal(data, &plan) != nil || plan.Target.Org != "org_b" {
		t.Fatal("saved plan lost the selected organization")
	}
	requests = 0
	stdout.Reset()
	stderr.Reset()
	arguments = append(arguments, "--org", "org_a")
	if code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies); code != 4 || requests != 0 || !strings.Contains(stdout.String(), "organization_mismatch") {
		t.Fatalf("conflicting organization was not rejected before requests: %d %s %s", code, &stdout, &stderr)
	}
	unchanged, _ := os.ReadFile(config)
	if !bytes.Equal(unchanged, original) {
		t.Fatal("planning modified the input manifest")
	}
}
