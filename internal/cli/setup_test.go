package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestWizardAndOfflineInputsProduceTheSameManifest(t *testing.T) {
	for _, test := range []struct {
		name, input string
		plans       []string
	}{
		{"one plan", "\n\n\n\nn\n", nil},
		{"several plans", "\n\n\ny\nPremium\n30\n\nn\n", []string{"plus=Plus:10", "premium=Premium:30"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			input := setupInput{ctx: context.Background(), reader: bufio.NewReader(strings.NewReader(test.input)), output: &output}
			guided, err := collectSetup(&input, setupOptions{name: "Demo", models: []string{"model_a"}, credits: "100"})
			if err != nil {
				t.Fatalf("guided setup: %v\n%s", err, output.String())
			}
			explicit, err := saasOptions("Demo", []string{"model_a"}, "100", test.plans)
			if err != nil {
				t.Fatal(err)
			}
			guidedJSON, _ := manifest.SaaS(guided)
			explicitJSON, _ := manifest.SaaS(explicit)
			if !bytes.Equal(guidedJSON, explicitJSON) {
				t.Fatal("guided and explicit input changed resource definitions")
			}
			if strings.Contains(output.String(), "JSON key") || strings.Contains(output.String(), "Reference") {
				t.Fatal("ordinary setup asked for its internal JSON entry names")
			}
		})
	}
}

func TestWizardExplainsReferenceCollisionsAndRetriesInvalidAllowances(t *testing.T) {
	options := manifest.SaaSOptions{Name: "Demo", Models: []string{"model_a"}, CreditsPerUSD: "100", Plans: []manifest.SaaSPlan{{Reference: "plus", Name: "Plus", Allowance: "10"}}}
	var output bytes.Buffer
	input := setupInput{ctx: context.Background(), reader: bufio.NewReader(strings.NewReader("Plus\npremium\nNaN\n30.000000000001\n")), output: &output}
	plan, err := collectSetupPlan(&input, options)
	if err != nil || plan.Reference != "premium" || plan.Allowance != "30.000000000001" {
		t.Fatalf("plan validation: %+v %v", plan, err)
	}
	if !strings.Contains(output.String(), "JSON key already used") || !strings.Contains(output.String(), "positive") || strings.Count(output.String(), "Plan name") != 1 {
		t.Fatalf("invalid input was not explained:\n%s", output.String())
	}
}

func wizardDependencies(t *testing.T) Dependencies {
	t.Helper()
	store := &testStore{present: true, login: auth.Login{APIOrigin: "https://api.example.test", ClientID: api.ClientID, Organization: "org_a", AccessToken: "private-token", Scope: defaultLoginScopes, ExpiresAt: time.Now().Add(time.Hour)}}
	dependencies := testDependencies(store)
	dependencies.InputIsTerminal = true
	dependencies.Auth.API.HTTP = &http.Client{Transport: requestHandler(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/me" {
			t.Fatalf("setup performed an unexpected operation: %s %s", request.Method, request.URL.Path)
		}
		writer := httptest.NewRecorder()
		_ = json.NewEncoder(writer).Encode(map[string]any{"user": map[string]string{"user_id": "user_a", "email": "user@example.test"}, "session": map[string]any{"authenticated": true, "expires_at": time.Now().Add(time.Hour)}, "orgs": []map[string]string{{"org_id": "org_a"}}})
		return writer.Result(), nil
	})}
	return dependencies
}

func TestSetupDeclineAndCancellationLeaveNoPartialManifest(t *testing.T) {
	for _, answer := range []string{"\n\n\n\nn\nn\n", ""} {
		config := filepath.Join(t.TempDir(), "zorveus.json")
		dependencies := wizardDependencies(t)
		dependencies.Stdin = strings.NewReader(answer)
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"setup", "--config", config, "--name", "Demo", "--model", "model_a"}, &stdout, &stderr, dependencies)
		if answer == "" && code != 130 || answer != "" && code != 0 {
			t.Fatalf("unexpected setup result: %d %s %s", code, stdout.String(), stderr.String())
		}
		if _, err := os.Stat(config); !os.IsNotExist(err) {
			t.Fatal("declined or canceled setup saved a partial manifest")
		}
	}
}

func TestSetupShowsTheCompletePlanBeforeApproval(t *testing.T) {
	config := filepath.Join(t.TempDir(), "zorveus.dev.json")
	dependencies := wizardDependencies(t)
	dependencies.Stdin = strings.NewReader("\n\n\n\nn\ny\nn\n")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"setup", "--config", config, "--name", "Demo", "--model", "model_a"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("setup: %d %s %s", code, stdout.String(), stderr.String())
	}
	for _, text := range []string{"https://api.example.test", "Organization", "org_a", `inference_key["plus"]`, `service_key["customers"]`, "Create these resources in your Zorveus account?", "No changes applied"} {
		if !strings.Contains(stdout.String(), text) {
			t.Errorf("missing %q in setup review", text)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(config), "zorveus.dev.plan.json")); err != nil {
		t.Fatal("environment-specific saved plan missing:", err)
	}
	applyCommand := "zorveus apply --plan " + quotePlanPath(filepath.Join(filepath.Dir(config), "zorveus.dev.plan.json")) + " --yes"
	if !strings.Contains(stdout.String(), applyCommand) {
		t.Fatal("declining apply did not show the command for the saved plan")
	}
	if strings.Contains(stdout.String()+stderr.String(), "private-token") {
		t.Fatal("login credential appeared in wizard output")
	}
}

func TestSetupNonInteractiveRequiresInputsWithoutPrompting(t *testing.T) {
	for _, arguments := range [][]string{
		{"setup", "--non-interactive", "--json"},
		{"setup", "--non-interactive", "--name", "Demo", "--model", "", "--json"},
		{"init", "--starter", "saas", "--model", " ", "--json"},
	} {
		config := filepath.Join(t.TempDir(), "zorveus.json")
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), append(arguments, "--config", config), &stdout, &stderr, Dependencies{})
		var result resultEnvelope
		if code != 2 || json.Unmarshal(stdout.Bytes(), &result) != nil || result.OK {
			t.Fatalf("missing inputs were accepted: %s %s", stdout.String(), stderr.String())
		}
		if _, err := os.Stat(config); !os.IsNotExist(err) {
			t.Fatal("invalid non-interactive input saved a manifest")
		}
	}
}

func TestSetupNonInteractiveWritesOneJSONResultAndDoesNotApplyWithoutYes(t *testing.T) {
	config := filepath.Join(t.TempDir(), "zorveus.json")
	dependencies := wizardDependencies(t)
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"setup", "--non-interactive", "--json", "--name", "Demo", "--config", config}, &stdout, &stderr, dependencies)
	var envelope struct {
		OK     bool        `json:"ok"`
		Result setupResult `json:"result"`
	}
	if code != 0 || json.Unmarshal(stdout.Bytes(), &envelope) != nil || !envelope.OK || envelope.Result.Applied || envelope.Result.Plan == "" {
		t.Fatalf("non-interactive setup result: %d %s %s", code, stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(config)
	if err != nil || !bytes.Contains(data, []byte(`"*"`)) {
		t.Fatal("non-interactive setup did not default to all models")
	}
}

func TestWizardDefaultsToAllModels(t *testing.T) {
	var output bytes.Buffer
	input := setupInput{ctx: context.Background(), reader: bufio.NewReader(strings.NewReader("\n\n\n\n\nn\n")), output: &output}
	options, err := collectSetup(&input, setupOptions{name: "Demo", credits: "100"})
	if err != nil || (len(options.Models) != 1 || options.Models[0] != "*") {
		t.Fatalf("wizard default: %+v %v", options, err)
	}
	if !strings.Contains(output.String(), "Models (* = all)") {
		t.Fatal("wizard did not explain the default model permissions")
	}
	data, err := manifest.SaaS(options)
	if err != nil {
		t.Fatal(err)
	}
	document, err := manifest.Parse(data)
	if err != nil || len(document.Resources("keys")) != 1 {
		t.Fatal("wildcard setup did not produce a valid plan configuration")
	}
}

func TestSetupReusesExistingManifestWithoutOverwritingIDs(t *testing.T) {
	config := filepath.Join(t.TempDir(), "zorveus.json")
	before := []byte(`{"version":1,"app":{"id":"app_a","name":"Demo","allowedModels":["model_a"]}}`)
	if err := os.WriteFile(config, before, 0600); err != nil {
		t.Fatal(err)
	}
	dependencies := wizardDependencies(t)
	dependencies.Stdin = strings.NewReader("n\nn\n")
	var stdout, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"setup", "--config", config}, &stdout, &stderr, dependencies); code != 0 {
		t.Fatalf("setup rerun: %d %s %s", code, stdout.String(), stderr.String())
	}
	after, _ := os.ReadFile(config)
	if !bytes.Equal(before, after) {
		t.Fatal("setup rerun changed existing IDs or configuration")
	}
}

func TestSetupPromptStopsWhenContextIsCanceled(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	input := setupInput{ctx: ctx, reader: bufio.NewReader(reader), output: io.Discard}
	if _, err := input.ask("App name", ""); err == nil || problem.Safe(err).Code != "setup_canceled" {
		t.Fatal("prompt ignored cancellation")
	}
}

func TestSaaSInitIsOfflineAndRefusesOverwrite(t *testing.T) {
	config := filepath.Join(t.TempDir(), "zorveus.json")
	arguments := []string{"init", "--starter", "saas", "--name", "Demo", "--model", "model_a", "--plan", "plus=Plus:10", "--plan", "premium=Premium:30", "--config", config, "--json"}
	var stdout, stderr bytes.Buffer
	if code := Execute(context.Background(), arguments, &stdout, &stderr, Dependencies{}); code != 0 {
		t.Fatalf("offline SaaS init failed: %s %s", stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := Execute(context.Background(), arguments, &stdout, &stderr, Dependencies{}); code != 1 || !strings.Contains(stdout.String(), "destination_exists") {
		t.Fatal("SaaS init overwrote an existing manifest")
	}
}

func TestSetupRejectsApplicationLaunchArguments(t *testing.T) {
	for _, arguments := range [][]string{
		{"setup", "--", "npm", "run", "dev"},
		{"setup", "--key", "plus"},
		{"setup", "--service-key", "customers"},
		{"setup", "--override-env"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Execute(context.Background(), arguments, &stdout, &stderr, Dependencies{}); code != 2 {
			t.Fatalf("setup accepted application launch inputs: %v, exit=%d %s %s", arguments, code, stdout.String(), stderr.String())
		}
	}
}
