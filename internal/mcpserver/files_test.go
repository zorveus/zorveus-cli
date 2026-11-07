package mcpserver

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func TestInitUsesCLIDefaultsAndRefusesExistingFiles(t *testing.T) {
	directory := t.TempDir()
	session := connect(t, Options{Directory: directory})
	initialized := call(t, session, "zorveus_init", map[string]any{})
	var result manifest.FileResult
	decode(t, initialized, &result)
	config := filepath.Join(directory, "zorveus.json")
	if initialized.IsError || result.Config != config || result.Version != 1 {
		t.Fatalf("init failed: %+v", initialized)
	}
	original, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		App struct {
			Name   string   `json:"name"`
			Models []string `json:"allowedModels"`
		} `json:"app"`
	}
	if json.Unmarshal(original, &document) != nil || document.App.Name != "My app" || len(document.App.Models) != 1 || document.App.Models[0] != "*" {
		t.Fatal("MCP init defaults differ from the CLI")
	}
	requireError(t, call(t, session, "zorveus_init", map[string]any{"name": "Replacement"}), "destination_exists")
	after, _ := os.ReadFile(config)
	if !bytes.Equal(original, after) {
		t.Fatal("init overwrote an existing manifest")
	}
	custom := call(t, session, "zorveus_init", map[string]any{"config": "custom.json", "name": "Custom", "models": []string{"model_a"}})
	if custom.IsError {
		t.Fatalf("custom init failed: %+v", custom)
	}
	data, _ := os.ReadFile(filepath.Join(directory, "custom.json"))
	if json.Unmarshal(data, &document) != nil || document.App.Name != "Custom" || len(document.App.Models) != 1 || document.App.Models[0] != "model_a" {
		t.Fatal("init did not pass through the supplied name and models")
	}
	for _, args := range []map[string]any{
		{"config": "invalid.json", "name": ""},
		{"config": "invalid.json", "models": []string{}},
	} {
		if !call(t, session, "zorveus_init", args).IsError {
			t.Fatal("init treated an explicit invalid value as an omitted default")
		}
		if _, err := os.Stat(filepath.Join(directory, "invalid.json")); !os.IsNotExist(err) {
			t.Fatal("invalid init created a file")
		}
	}
}

func TestCloneAndPromoteResolvePathsAndPreserveResourceIdentity(t *testing.T) {
	directory := t.TempDir()
	source := []byte(`{
  "version": 1,
  "app": {"id":"app_dev","name":"Development","allowedModels":["*"]},
  "keys": {"backend":{"id":"key_dev","name":"Backend dev"}},
  "routingKeys": {"primary":{"id":"router_dev","name":"Router dev","members":["backend"]}}
}`)
	if err := os.WriteFile(filepath.Join(directory, "dev.json"), source, 0600); err != nil {
		t.Fatal(err)
	}
	session := connect(t, Options{Directory: directory})
	cloned := call(t, session, "zorveus_clone", map[string]any{"from": "dev.json", "to": "copy.json", "name": "New app"})
	var result manifest.FileResult
	decode(t, cloned, &result)
	if cloned.IsError || result.Config != filepath.Join(directory, "copy.json") {
		t.Fatalf("clone failed: %+v", cloned)
	}
	copyBytes, _ := os.ReadFile(result.Config)
	if bytes.Contains(copyBytes, []byte(`"id"`)) || !bytes.Contains(copyBytes, []byte("New app")) || !bytes.Contains(copyBytes, []byte("backend")) {
		t.Fatal("clone did not strip app-owned IDs while keeping names and selectors")
	}
	requireError(t, call(t, session, "zorveus_clone", map[string]any{"from": "dev.json", "to": "copy.json"}), "destination_exists")
	for _, args := range []map[string]any{
		{"from": "", "to": "invalid.json"},
		{"from": "dev.json", "to": "invalid.json", "name": ""},
	} {
		requireError(t, call(t, session, "zorveus_clone", args), "invalid_arguments")
	}
	// Matching object names preserve production IDs in either name-copying mode.
	for _, keepNames := range []bool{false, true} {
		target := filepath.Join(directory, "prod.json")
		if err := os.WriteFile(target, []byte(`{
  "version": 1,
  "app": {"id":"app_prod","name":"Production","allowedModels":["*"]},
  "keys": {"backend":{"id":"key_prod","name":"Backend prod"}},
  "routingKeys": {"primary":{"id":"router_prod","name":"Router prod","members":["backend"]}}
}`), 0600); err != nil {
			t.Fatal(err)
		}
		promoted := call(t, session, "zorveus_promote", map[string]any{"from": "dev.json", "to": "prod.json", "keep_names": keepNames})
		var promotion provision.PromotionResult
		decode(t, promoted, &promotion)
		if promoted.IsError || promotion.Config != target || promotion.Source != filepath.Join(directory, "dev.json") {
			t.Fatalf("promotion failed: %+v", promoted)
		}
		data, _ := os.ReadFile(target)
		var document struct {
			App  struct{ ID, Name string }
			Keys map[string]struct{ ID, Name string }
		}
		if json.Unmarshal(data, &document) != nil || document.App.ID != "app_prod" || document.Keys["backend"].ID != "key_prod" || !bytes.Contains(data, []byte("router_prod")) || bytes.Contains(data, []byte("key_dev")) {
			t.Fatal("promotion replaced target identities")
		}
		wantName, wantKeyName := "Development", "Backend dev"
		if keepNames {
			wantName, wantKeyName = "Production", "Backend prod"
		}
		if document.App.Name != wantName || document.Keys["backend"].Name != wantKeyName {
			t.Fatal("promotion ignored keep_names")
		}
	}
	requireError(t, call(t, session, "zorveus_promote", map[string]any{"from": "dev.json", "to": ""}), "invalid_arguments")
	requireError(t, call(t, session, "zorveus_promote", map[string]any{"from": "dev.json", "to": "dev.json"}), "promotion_same_file")
	after, _ := os.ReadFile(filepath.Join(directory, "dev.json"))
	if !bytes.Equal(source, after) {
		t.Fatal("clone or promote changed the source manifest")
	}
}
