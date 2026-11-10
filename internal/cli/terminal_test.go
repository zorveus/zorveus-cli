package cli

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/provision"
)

func TestWizardColorsRespectOutputAndOptOuts(t *testing.T) {
	for _, test := range []struct {
		name                              string
		terminal, noColorFlag, jsonOutput bool
		noColorEnv, term                  string
		wantColor                         bool
	}{
		{name: "terminal", terminal: true, term: "xterm-256color", wantColor: true},
		{name: "redirected", term: "xterm-256color"},
		{name: "explicit opt out", terminal: true, noColorFlag: true, term: "xterm-256color"},
		{name: "environment opt out", terminal: true, noColorEnv: "1", term: "xterm-256color"},
		{name: "dumb terminal", terminal: true, term: "dumb"},
		{name: "JSON", terminal: true, jsonOutput: true, term: "xterm-256color"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", test.noColorEnv)
			t.Setenv("TERM", test.term)
			dependencies := wizardDependencies(t)
			dependencies.OutputIsTerminal = test.terminal
			dependencies.ErrorIsTerminal = test.terminal
			dependencies.Stdin = strings.NewReader("\n\n\n\n\ny\nn\n")
			arguments := []string{"setup", "--config", filepath.Join(t.TempDir(), "zorveus.json"), "--name", "Show room"}
			if test.noColorFlag {
				arguments = append(arguments, "--no-color")
			}
			if test.jsonOutput {
				arguments = append(arguments, "--non-interactive", "--json")
			}
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies)
			if code != 0 || strings.Contains(stdout.String()+stderr.String(), "\x1b[") != test.wantColor {
				t.Fatalf("unexpected terminal output: %d %q %q", code, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), "private-token") {
				t.Fatal("wizard displayed a login credential")
			}
		})
	}
}

func TestSetupReviewKeepsEveryChangedValueAndDestructiveWarning(t *testing.T) {
	result := provision.PlanResult{Plan: "zorveus.plan.json", Changes: []provision.Step{
		{Kind: "create_app", Ref: "app", After: planTestFields(`{"app_name":"Show room","allowed_models":["*"],"credit_units_per_usd":"1000","client_type":"confidential"}`)},
		{Kind: "key_cap", Ref: "key:plus", Before: planTestFields(`{"amount":"10","currency":"USD","period":"monthly"}`), After: planTestFields(`{"amount":"30.000000000001","currency":"USD","period":"monthly"}`)},
		{Kind: "revoke_key", Ref: "key:old", ID: "connection_old", Before: planTestFields(`{"status":"active"}`), After: planTestFields(`{"status":"revoked"}`), Destructive: true},
	}}
	output := formatSetupPlan(result, false)
	for _, value := range []string{"Show room", "All models (*)", "confidential", "1000", "10 → 30.000000000001", "connection_old", "active → revoked", "--allow-delete", "1 to add, 1 to change, 1 to remove", "zorveus.plan.json"} {
		if !strings.Contains(output, value) {
			t.Errorf("review omitted %q:\n%s", value, output)
		}
	}
	if strings.Contains(output, "zorveus apply") || strings.Contains(output, "known after apply") || strings.Contains(output, "\x1b[") {
		t.Fatal("wizard repeated instructions or included terminal escapes in plain output")
	}
}

func TestPromptEscapesUntrustedLabelsAndDefaults(t *testing.T) {
	var output bytes.Buffer
	input := setupInput{ctx: context.Background(), reader: bufio.NewReader(strings.NewReader("\n")), output: &output, color: true}
	if _, err := input.ask("Name\x1b[31m", "Unsafe\x1b[31m"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "\x1b[31m") || !strings.Contains(output.String(), "\x1b[36m") {
		t.Fatal("prompt allowed input to control its terminal styling")
	}
}

func TestSetupReviewShowsSecretSourcesWithoutReadingTheirValues(t *testing.T) {
	t.Setenv("TEST_PROVIDER_SECRET", "private-provider-secret")
	output := formatSetupPlan(provision.PlanResult{Plan: "review.plan.json", Changes: []provision.Step{{Kind: "provider_secret", Ref: "provider:openai", SecretEnv: "TEST_PROVIDER_SECRET", Before: planTestFields(`{"active_secret_version_id":"version_old"}`), After: planTestFields(`{"rotation_requested":true}`)}}}, true)
	if !strings.Contains(output, "TEST_PROVIDER_SECRET") || !strings.Contains(output, "version_old → rotated on apply") || strings.Contains(output, "private-provider-secret") {
		t.Fatal("review changed secret handling")
	}
}

// Echo one line per read, like a terminal. This keeps the preview faithful to
// prompt timing rather than echoing all answers before the first question.
type terminalAnswers struct {
	input  *strings.Reader
	output io.Writer
}

func (answers terminalAnswers) Read(buffer []byte) (int, error) {
	for index := range buffer {
		character, err := answers.input.ReadByte()
		if err != nil {
			return index, err
		}
		buffer[index] = character
		_, _ = answers.output.Write(buffer[index : index+1])
		if character == '\n' {
			return index + 1, nil
		}
	}
	return len(buffer), nil
}

func TestWizardPresentationPreview(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	var stdout, stderr bytes.Buffer
	dependencies := wizardDependencies(t)
	dependencies.OutputIsTerminal = true
	dependencies.Stdin = terminalAnswers{input: strings.NewReader("Show room\n\n1000\n\n\n\ny\nn\n"), output: &stdout}
	code := Execute(context.Background(), []string{"setup"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("wizard preview: %d %s", code, stderr.String())
	}
	beforeReview, _, _ := strings.Cut(stdout.String(), "Review")
	if !strings.Contains(beforeReview, "10,000 credits") || readableCredits("1234567.000000000001") != "1,234,567.000000000001" {
		t.Fatal("credit display changed precision or omitted digit grouping")
	}
	if strings.Contains(beforeReview, "subscription") || strings.Contains(beforeReview, "backend") || strings.Contains(beforeReview, "does not check") {
		t.Fatal("wizard prompts contain documentation paragraphs")
	}
	if destination := os.Getenv("ZORVEUS_TEST_PREVIEW"); destination != "" {
		if err := os.WriteFile(destination, stdout.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
