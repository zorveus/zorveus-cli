package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineManifestWorkflowNeedsNoAuthenticationDependencies(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "zorveus.json")
	destination := filepath.Join(directory, "zorveus.dev.json")
	commands := [][]string{
		{"init", "--config", source, "--name", "Assistant", "--non-interactive", "--json"},
		{"validate", "--config", source, "--non-interactive", "--json"},
		{"clone", "--from", source, "--to", destination, "--name", "Development", "--non-interactive", "--json"},
		{"validate", "--config", destination, "--non-interactive", "--json"},
	}
	for _, arguments := range commands {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), arguments, &stdout, &stderr, Dependencies{})
		if code != 0 {
			t.Fatalf("%s failed: exit %d, %s\n%s", arguments[0], code, stderr.String(), stdout.String())
		}
		var result resultEnvelope
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || !result.OK || result.Result == nil {
			t.Fatalf("expected one successful JSON result, got %s", stdout.String())
		}
	}
	data, err := os.ReadFile(destination)
	if err != nil || !bytes.Contains(data, []byte("Development")) {
		t.Fatal("clone did not save the replacement app name")
	}
	var created struct {
		App struct {
			AllowedModels []string `json:"allowedModels"`
		} `json:"app"`
	}
	if err := json.Unmarshal(data, &created); err != nil || len(created.App.AllowedModels) != 1 || created.App.AllowedModels[0] != "*" {
		t.Fatal("init did not default to all models, or clone changed that default")
	}
}

func TestValidationReportsSafeErrorsWithPointers(t *testing.T) {
	config := filepath.Join(t.TempDir(), "zorveus.json")
	data := []byte(`{"version":1,"app":{"name":"Example","allowedModels":["model_a"],"secret":"private-secret"}}`)
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, jsonMode := range []bool{false, true} {
		arguments := []string{"validate", "--config", config}
		if jsonMode {
			arguments = append(arguments, "--json")
		}
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), arguments, &stdout, &stderr, Dependencies{})
		if code != 2 || strings.Contains(stdout.String()+stderr.String(), "private-secret") {
			t.Fatal("validation did not return a safe input error")
		}
		if !jsonMode {
			if !strings.Contains(stderr.String(), "/app/secret") || stdout.Len() != 0 {
				t.Fatal("human validation error did not identify the JSON location")
			}
			continue
		}
		var result resultEnvelope
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Error == nil || result.Error.Pointer == nil || *result.Error.Pointer != "/app/secret" || result.Error.Code != "unknown_field" {
			t.Fatalf("validation error did not include a stable code and pointer: %s", stdout.String())
		}
	}
}

func TestCloneRequiresBothPaths(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"clone", "--from", "source.json", "--json"}, &stdout, &stderr, Dependencies{})
	if code != 2 || !strings.Contains(stdout.String(), "invalid_arguments") {
		t.Fatal("clone accepted a missing destination")
	}
}
