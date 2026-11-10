package cli

import (
	"bufio"
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/manifest"
)

func TestWizardRoutingUsesCollectedPlanReferences(t *testing.T) {
	var output bytes.Buffer
	answers := "\n\n\ny\nPro\n30\nn\ny\n\nunknown\npro\n"
	input := setupInput{ctx: context.Background(), reader: bufio.NewReader(strings.NewReader(answers)), output: &output}
	options, err := collectSetup(&input, setupOptions{name: "Demo", models: []string{"*"}, credits: "100"})
	if err != nil {
		t.Fatalf("wizard routing failed: %v\n%s", err, &output)
	}
	data, err := manifest.SaaS(options)
	if err != nil {
		t.Fatal(err)
	}
	document, err := manifest.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	routers := document.Resources("routingKeys")
	if len(routers) != 1 || routers[0].Name != "primary" || routers[0].Fields["name"] != "Demo router" || routers[0].Fields["defaultSelector"] != "pro" || !reflect.DeepEqual(routers[0].Fields["members"], []any{"plus", "pro"}) {
		t.Fatal("wizard routing did not use the collected references")
	}
	for _, text := range []string{"Create a routing key? (y/N)", "Selectors", "plus, pro", "Choose one of: plus, pro"} {
		if !strings.Contains(output.String(), text) {
			t.Errorf("routing omitted %q", text)
		}
	}
	if strings.Contains(output.String(), "Reference") || strings.Contains(output.String(), "JSON key") {
		t.Fatal("routing asked for references already collected by the wizard")
	}
}

func TestExistingRoutingRequiresApprovalToUpdate(t *testing.T) {
	options := manifest.SaaSOptions{Name: "Demo", Plans: []manifest.SaaSPlan{{Reference: "plus"}, {Reference: "pro"}}}
	existing := &manifest.SaaSRouting{Name: "Existing router", DefaultSelector: "pro"}
	for _, approved := range []bool{false, true} {
		answers := "\n"
		if approved {
			answers = "y\n\n\n"
		}
		var output bytes.Buffer
		input := setupInput{ctx: context.Background(), reader: bufio.NewReader(strings.NewReader(answers)), output: &output}
		result, err := collectSetupRouting(&input, setupOptions{}, options, existing)
		if err != nil {
			t.Fatal(err)
		}
		if !approved && result.Routing != nil {
			t.Fatal("existing router was changed without approval")
		}
		if approved && (result.Routing == nil || *result.Routing != *existing) {
			t.Fatal("existing routing name or default was not used")
		}
		if !strings.Contains(output.String(), "Update routing key to include all plans? (y/N)") {
			t.Fatal("rewrite did not explain the routing change")
		}
	}
}

func TestSetupRoutingAppearsInReviewAndWorksWithoutPrompts(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		config := filepath.Join(t.TempDir(), "zorveus.json")
		dependencies := wizardDependencies(t)
		arguments := []string{"setup", "--config", config, "--name", "Demo", "--plan", "plus=Plus:10", "--plan", "pro=Pro:30"}
		if interactive {
			dependencies.Stdin = strings.NewReader("\n\ny\n\npro\ny\nn\n")
		} else {
			arguments = append(arguments, "--non-interactive", "--routing-key", "--default-selector", "pro")
		}
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies)
		if code != 0 {
			t.Fatalf("setup routing failed: %d %s %s", code, &stdout, &stderr)
		}
		document, err := manifest.Load(config)
		if err != nil {
			t.Fatal(err)
		}
		routers := document.Resources("routingKeys")
		if len(routers) != 1 || routers[0].Fields["defaultSelector"] != "pro" || !strings.Contains(stdout.String(), `routing_key["primary"]`) {
			t.Fatalf("router was not saved and reviewed: %s", &stdout)
		}
	}
}

func TestSetupRejectsInvalidRoutingFlagsBeforeLogin(t *testing.T) {
	for _, extra := range [][]string{{"--default-selector", "plus"}, {"--routing-key", "--default-selector", "missing"}} {
		arguments := []string{"setup", "--non-interactive", "--name", "Demo", "--config", filepath.Join(t.TempDir(), "zorveus.json")}
		arguments = append(arguments, extra...)
		var stdout, stderr bytes.Buffer
		if code := Execute(context.Background(), arguments, &stdout, &stderr, Dependencies{}); code != 2 {
			t.Fatalf("invalid routing options reached login: %d %s %s", code, &stdout, &stderr)
		}
	}
}
