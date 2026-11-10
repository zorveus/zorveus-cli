package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/manifest"
)

func existingDraft(t *testing.T) string {
	t.Helper()
	config := filepath.Join(t.TempDir(), "zorveus.json")
	_, err := manifest.InitSaaS(config, manifest.SaaSOptions{Name: "Show room", Models: []string{"model_a", "model_b"}, CreditsPerUSD: "1000", Plans: []manifest.SaaSPlan{{Reference: "plus", Name: "Plus", Allowance: "10"}}})
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestExistingSetupShowsDetailsThenAcceptsYesOrNo(t *testing.T) {
	config := existingDraft(t)
	before, _ := os.ReadFile(config)
	dependencies := wizardDependencies(t)
	dependencies.Stdin = strings.NewReader("y\nn\n")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"setup", "--config", config}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("reuse: %d %s %s", code, stdout.String(), stderr.String())
	}
	for _, text := range []string{"Existing configuration", "Show room", "model_a, model_b", "1000", "Plan: Plus", "10 USD / monthly per customer", "Use this configuration?"} {
		if !strings.Contains(stdout.String(), text) {
			t.Errorf("existing details omitted %q", text)
		}
	}
	if strings.Index(stdout.String(), "Show room") > strings.Index(stdout.String(), "Use this configuration?") || strings.Contains(stdout.String(), "review / check / quit") {
		t.Fatal("reuse prompted before showing details or still uses the old menu")
	}
	after, _ := os.ReadFile(config)
	if !bytes.Equal(before, after) {
		t.Fatal("reusing an unapplied configuration changed its file")
	}
}

func TestExistingSetupCanRewriteWithDefaultsAndBackup(t *testing.T) {
	config := existingDraft(t)
	before, _ := os.ReadFile(config)
	dependencies := wizardDependencies(t)
	dependencies.Stdin = strings.NewReader("n\ny\nUpdated room\n\n\nPremium\n25\nn\nn\ny\nn\n")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"setup", "--config", config}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("rewrite: %d %s %s", code, stdout.String(), stderr.String())
	}
	document, err := manifest.Load(config)
	if err != nil {
		t.Fatal(err)
	}
	key := document.Resources("keys")[0]
	if key.Name != "plus" || key.Fields["name"] != "Premium" || key.Fields["cap"].(map[string]any)["amount"] != "25" {
		t.Fatal("rewrite failed to retain the plan reference or change its settings")
	}
	if !strings.Contains(stdout.String(), "model_a, model_b") || !strings.Contains(stdout.String(), "Rewrite this configuration?") {
		t.Fatal("rewrite did not preserve model defaults or ask for confirmation")
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(config), ".zorveus", "setup-backup-*", "zorveus.json"))
	if len(backups) != 1 {
		t.Fatal("rewrite did not preserve the original file")
	}
	data, _ := os.ReadFile(backups[0])
	if !bytes.Equal(before, data) {
		t.Fatal("rewrite backup differs from the original")
	}
}

func TestCancelingRewriteKeepsOriginalFileWithoutBackup(t *testing.T) {
	config := existingDraft(t)
	before, _ := os.ReadFile(config)
	dependencies := wizardDependencies(t)
	dependencies.Stdin = strings.NewReader("n\ny\n" + strings.Repeat("\n", 5) + "n\nn\nn\n")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"setup", "--config", config}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("canceled rewrite: %d %s %s", code, stdout.String(), stderr.String())
	}
	after, _ := os.ReadFile(config)
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(config), ".zorveus", "setup-backup-*"))
	if !bytes.Equal(before, after) || len(backups) != 0 {
		t.Fatal("canceling the rewrite changed or backed up the original file")
	}
}
