package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func planTestFields(value string) provision.Fields {
	var fields provision.Fields
	if err := json.Unmarshal([]byte(value), &fields); err != nil {
		panic(err)
	}
	return fields
}

func TestPlanOutputExplainsChangesAndDestructiveActions(t *testing.T) {
	result := provision.PlanResult{Plan: "review.plan.json", Changes: []provision.Step{
		{Kind: "create_app", Ref: "app", After: planTestFields(`{"app_name":"Assistant","allowed_models":["*"],"client_type":"confidential"}`)},
		{Kind: "key_cap", Ref: "key:backend", After: planTestFields(`{"amount":"100","currency":"USD","period":"monthly"}`)},
		{Kind: "app_profile", Ref: "app", ID: "app_existing", Before: planTestFields(`{"app_name":"Old name","credit_units_per_usd":"1000.000000000000"}`), After: planTestFields(`{"app_name":"New name","credit_units_per_usd":"1000"}`)},
		{Kind: "revoke_key", Ref: "id:connection_old", ID: "connection_old", Before: planTestFields(`{"status":"active"}`), After: planTestFields(`{"status":"revoked"}`), Destructive: true},
	}}
	output := formatPlan(result, false)
	for _, expected := range []string{
		`# app will be created`,
		`+ app {`,
		`id = (known after apply)`,
		`+ name = "Assistant"`,
		`+ allowed_models = ["*"]`,
		`# cap["backend"] will be created`,
		`~ name = "Old name" -> "New name"`,
		`# 1 unchanged field hidden`,
		`# inference_key["connection_old"] will be revoked`,
		`- status = "active" -> "revoked"`,
		`Plan: 2 to add, 1 to change, 1 to remove.`,
		`zorveus apply --plan review.plan.json --yes --allow-delete`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in plan:\n%s", expected, output)
		}
	}
	if strings.Contains(output, "credit_units_per_usd") || strings.Contains(output, "\x1b") || strings.Contains(output, "before:") {
		t.Fatalf("plan included unchanged values, escapes, or the old raw output:\n%s", output)
	}
}

func TestApplyHintQuotesPlanPathsWithoutShellExpansion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this check uses a POSIX shell")
	}
	for _, path := range []string{"review.plan.json", "plan with spaces.json", "Peter's plan.json", "$(printf expanded).json", "#plan.json", "~/plan.json", "plan\x1b[31m.json"} {
		command := exec.Command("sh", "-c", "printf '%s' "+quotePlanPath(path))
		output, err := command.Output()
		if err != nil || string(output) != terminalText(path) {
			t.Fatalf("apply hint changed path %q: got %q, error %v", path, output, err)
		}
	}
}

func TestPlanOutputShowsSecretSourcesWithoutReadingSecrets(t *testing.T) {
	t.Setenv("TEST_PROVIDER_SECRET", "private-provider-secret")
	result := provision.PlanResult{Plan: "rotate.plan.json", Changes: []provision.Step{{
		Kind: "provider_secret", Ref: "provider:0", ID: "provider_existing", SecretEnv: "TEST_PROVIDER_SECRET",
		Before: planTestFields(`{"active_secret_version_id":"version_old"}`), After: planTestFields(`{"rotation_requested":true}`),
	}}}
	output := formatPlan(result, true)
	if !strings.Contains(output, "will have its stored secret rotated") || !strings.Contains(output, `secret_from_env = "TEST_PROVIDER_SECRET"`) || !strings.Contains(output, `"version_old" -> (known after apply)`) {
		t.Fatalf("secret rotation was not explained:\n%s", output)
	}
	if strings.Contains(output, "private-provider-secret") {
		t.Fatal("plan output read or exposed a provider secret")
	}
}

func TestPlanOutputPreservesPrecisionAndEscapesTerminalControls(t *testing.T) {
	raw := json.RawMessage(`{"amount":1234567890123456789,"name":"Assistant\u001b[31m\n"}`)
	value := formatPlanValue(raw)
	if !strings.Contains(value, "1234567890123456789") || strings.ContainsAny(value, "\x1b\r") || strings.Contains(value, "Assistant\n") {
		t.Fatalf("numeric precision or terminal safety changed: %q", value)
	}
}

func TestNoChangePlanHasNoApplyPrompt(t *testing.T) {
	output := formatPlan(provision.PlanResult{Plan: "check.plan.json"}, false)
	if !strings.Contains(output, "No changes.") || !strings.Contains(output, "Saved plan: check.plan.json") || strings.Contains(output, "zorveus apply") {
		t.Fatalf("unexpected empty plan:\n%s", output)
	}
}

func TestPlanColorsRespectTerminalAndExplicitOptOuts(t *testing.T) {
	cases := []struct {
		name      string
		terminal  bool
		noColor   string
		term      string
		arguments []string
		colored   bool
	}{
		{"terminal", true, "", "xterm-256color", nil, true},
		{"piped", false, "", "xterm-256color", nil, false},
		{"flag", true, "", "xterm-256color", []string{"--no-color"}, false},
		{"environment", true, "1", "xterm-256color", nil, false},
		{"dumb terminal", true, "", "dumb", nil, false},
		{"JSON", true, "", "xterm-256color", []string{"--json"}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", test.noColor)
			t.Setenv("TERM", test.term)
			store := &testStore{present: true, login: auth.Login{APIOrigin: "https://api.example.test", ClientID: api.ClientID, Organization: "org_a", AccessToken: "private-token", Scope: "apps:read", ExpiresAt: time.Now().Add(time.Hour)}}
			dependencies := testDependencies(store)
			dependencies.OutputIsTerminal = test.terminal
			dependencies.Auth.API.HTTP.Transport = requestHandler(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodGet || request.URL.Path != "/me" {
					t.Fatalf("plan attempted an unexpected request: %s %s", request.Method, request.URL.Path)
				}
				body := fmt.Sprintf(`{"user":{"user_id":"user_a","email":"user@example.test"},"session":{"authenticated":true,"expires_at":%q},"orgs":[{"org_id":"org_a"}]}`, time.Now().Add(time.Hour).Format(time.RFC3339))
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			directory := t.TempDir()
			config, plan := filepath.Join(directory, "zorveus.json"), filepath.Join(directory, "plan.json")
			if err := os.WriteFile(config, []byte(`{"version":1,"app":{"name":"Assistant","allowedModels":["*"]}}`), 0600); err != nil {
				t.Fatal(err)
			}
			arguments := append([]string{"plan", "--config", config, "--out", plan}, test.arguments...)
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies)
			if code != 0 || strings.Contains(stdout.String(), "\x1b[") != test.colored {
				t.Fatalf("unexpected color output: exit %d, stdout %q, stderr %s", code, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), "private-token") {
				t.Fatal("plan output exposed a management credential")
			}
			if test.name == "JSON" {
				var result resultEnvelope
				if json.Unmarshal(stdout.Bytes(), &result) != nil || !result.OK {
					t.Fatal("color support changed JSON output")
				}
			}
		})
	}
}
