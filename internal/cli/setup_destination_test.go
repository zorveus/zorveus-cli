package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func savedSetupState(t *testing.T, config string) (string, []byte) {
	t.Helper()
	state := provision.State{Version: 1, Target: provision.Target{Origin: "https://api.example.test", Org: "org_a", User: "user_a"}, AppID: "app_previous", Keys: map[string]string{"backend": "conn_previous"}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(filepath.Dir(config), ".zorveus")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, filepath.Base(config)+".state.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

func TestSetupDetectsOldStateBeforeCollectingNewAnswers(t *testing.T) {
	config := filepath.Join(t.TempDir(), "zorveus.json")
	statePath, stateBefore := savedSetupState(t, config)
	dependencies := wizardDependencies(t)
	dependencies.Stdin = strings.NewReader("y\n\n\n\n\nn\nn\n")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"setup", "--config", config, "--name", "Demo", "--model", "model_a"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("setup conflict: %d %s %s", code, stdout.String(), stderr.String())
	}
	output := stdout.String()
	if strings.Index(output, "Start a new app?") >= strings.Index(output, "Credits per USD") || !strings.Contains(output, "Start a new app?") {
		t.Fatal("state conflict was detected after collecting answers")
	}
	for _, path := range []string{config, filepath.Join(filepath.Dir(config), "zorveus.new.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("declined setup wrote %s", path)
		}
	}
	checkStateBackup(t, statePath, stateBefore)
}

func TestSetupKeepsSavedAnswersInTheSameFileWhenStartingFresh(t *testing.T) {
	config := filepath.Join(t.TempDir(), "zorveus.json")
	statePath, stateBefore := savedSetupState(t, config)
	options := manifest.SaaSOptions{Name: "Show room", CreditsPerUSD: "1000", Plans: []manifest.SaaSPlan{{Reference: "plus", Name: "Plus", Allowance: "1000"}, {Reference: "pro", Name: "Pro", Allowance: "5"}}}
	if _, err := manifest.InitSaaS(config, options); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(config)
	dependencies := wizardDependencies(t)
	dependencies.Stdin = strings.NewReader("y\nn\n")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"setup", "--config", config}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("saved answer recovery: %d %s %s", code, stdout.String(), stderr.String())
	}
	after, _ := os.ReadFile(config)
	if !bytes.Equal(before, after) {
		t.Fatal("recovery changed the saved answers")
	}
	checkStateBackup(t, statePath, stateBefore)
	document, err := manifest.Load(config)
	if err != nil || document.AppID() != "" {
		t.Fatal("recovery adopted the old app identity")
	}
	canonical, _ := document.Bytes()
	if !bytes.Equal(before, canonical) || len(document.Resources("keys")) != 2 {
		t.Fatal("recovery changed the saved plans or allowances")
	}
	if strings.Contains(stdout.String(), "App name") || !strings.Contains(stdout.String(), "Review") || !strings.Contains(stdout.String(), "1000") {
		t.Fatal("recovery repeated the wizard instead of reviewing saved answers")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(config), "zorveus.new.json")); !os.IsNotExist(err) {
		t.Fatal("recovery created another manifest")
	}
}

func checkStateBackup(t *testing.T, statePath string, expected []byte) {
	t.Helper()
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatal("conflicting state still occupies the active state path")
	}
	paths, err := filepath.Glob(strings.TrimSuffix(statePath, ".state.json") + ".state.backup-*.json")
	if err != nil || len(paths) != 1 {
		t.Fatal("expected one preserved state backup")
	}
	data, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(expected, data) {
		t.Fatal("previous state was not preserved exactly")
	}
}

func TestCancelingSetupRecoveryKeepsStateAndAnswers(t *testing.T) {
	config := filepath.Join(t.TempDir(), "zorveus.json")
	statePath, stateBefore := savedSetupState(t, config)
	dependencies := wizardDependencies(t)
	dependencies.Stdin = strings.NewReader("n\n")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"setup", "--config", config}, &stdout, &stderr, dependencies)
	if code != 0 || strings.Contains(stdout.String(), "App name") {
		t.Fatalf("canceled recovery: %d %s %s", code, stdout.String(), stderr.String())
	}
	stateAfter, _ := os.ReadFile(statePath)
	if !bytes.Equal(stateBefore, stateAfter) {
		t.Fatal("canceling recovery changed the active state")
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatal("canceling recovery created a manifest")
	}
}

func TestNonInteractiveSetupPreservesConflictingStateAndCreatesNoManifest(t *testing.T) {
	config := filepath.Join(t.TempDir(), "zorveus.json")
	statePath, before := savedSetupState(t, config)
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"setup", "--non-interactive", "--json", "--config", config, "--name", "Demo"}, &stdout, &stderr, wizardDependencies(t))
	var result resultEnvelope
	if code != 4 || json.Unmarshal(stdout.Bytes(), &result) != nil || result.Error == nil || result.Error.Code != "state_target_mismatch" {
		t.Fatalf("non-interactive conflict: %d %s %s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatal("non-interactive setup saved a conflicting manifest")
	}
	after, _ := os.ReadFile(statePath)
	if !bytes.Equal(before, after) {
		t.Fatal("non-interactive setup overwrote the old state")
	}
}

func TestWizardAsksForJSONKeyOnlyWhenNameCannotBeConverted(t *testing.T) {
	options := manifest.SaaSOptions{Name: "Demo", Models: []string{"*"}, CreditsPerUSD: "100"}
	var output bytes.Buffer
	input := setupInput{ctx: context.Background(), reader: bufio.NewReader(strings.NewReader("✨\ncustom\n10\n")), output: &output}
	plan, err := collectSetupPlan(&input, options)
	if err != nil || plan.Name != "✨" || plan.Reference != "custom" || strings.Count(output.String(), "? JSON key") != 1 {
		t.Fatalf("unconvertible name: %+v %v %s", plan, err, output.String())
	}
}

func TestSetupDoesNotStartOverWhenAnApplyHasNoManifest(t *testing.T) {
	config := filepath.Join(t.TempDir(), "zorveus.json")
	directory := filepath.Join(filepath.Dir(config), ".zorveus")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	journal := provision.Journal{Version: 1, Config: config, Complete: false}
	before, _ := json.Marshal(journal)
	path := filepath.Join(directory, "previous.apply.json")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"setup", "--non-interactive", "--json", "--config", config, "--name", "Demo"}, &stdout, &stderr, wizardDependencies(t))
	var result resultEnvelope
	if code != 5 || json.Unmarshal(stdout.Bytes(), &result) != nil || result.Error == nil || result.Error.Code != "apply_pending" {
		t.Fatalf("unfinished apply was bypassed: %d %s %s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatal("setup started a fresh manifest over unfinished work")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("setup changed the unfinished apply journal")
	}
}
