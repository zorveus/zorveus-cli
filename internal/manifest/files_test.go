package manifest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestInitCreatesAValidFileAndNeverOverwritesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zorveus.json")
	_, err := Init(path, "My assistant", []string{"model_a", "model_b"})
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal("init produced an invalid manifest:", err)
	}
	original := readFile(t, path)
	if _, err := Init(path, "Another app", []string{"model_c"}); err == nil || problem.Safe(err).Code != "destination_exists" {
		t.Fatal("init did not refuse an existing destination")
	}
	if !bytes.Equal(original, readFile(t, path)) {
		t.Fatal("init changed an existing file")
	}
}

func TestInitRejectsInvalidInputBeforeCreatingAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zorveus.json")
	if _, err := Init(path, " ", []string{"model_a"}); err == nil {
		t.Fatal("blank app name was accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid init created a destination")
	}
}

func TestInitKeepsExplicitModelTextReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zorveus.json")
	_, err := Init(path, "Example", []string{"<model-id>"})
	if err != nil {
		t.Fatalf("could not create the manifest: %v", err)
	}
	if !bytes.Contains(readFile(t, path), []byte(`"<model-id>"`)) {
		t.Fatal("the model placeholder is not readable in the generated file")
	}
}

func TestCloneClearsOwnedIDsAndPreservesSourceAndConfiguration(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "prod.json")
	destination := filepath.Join(directory, "dev.json")
	input := []byte(`{
  "app": {
    "id": "app_prod",
    "name": "Production",
    "allowedModels": [
      "model_a"
    ],
    "creditUnitsPerUsd": "1000.000000000001"
  },
  "keys": {
    "backend": {
      "cap": null,
      "id": "key_prod",
      "name": "Backend",
      "productUserChargeMultiplier": "1.000001"
    }
  },
  "providerCredentials": {},
  "routingKeys": {
    "public": {
      "defaultSelector": null,
      "id": "router_prod",
      "members": [
        "backend"
      ],
      "name": "Router"
    }
  },
  "version": 1
}`)
	if err := os.WriteFile(source, input, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Clone(source, destination, "Development"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(input, readFile(t, source)) {
		t.Fatal("clone modified the source file")
	}
	cloned, err := Load(destination)
	if err != nil {
		t.Fatal(err)
	}
	app := cloned.data["app"].(map[string]any)
	key := cloned.data["keys"].(map[string]any)["backend"].(map[string]any)
	router := cloned.data["routingKeys"].(map[string]any)["public"].(map[string]any)
	for _, resource := range []map[string]any{app, key, router} {
		if _, exists := resource["id"]; exists {
			t.Fatal("an app-owned ID survived cloning")
		}
	}
	if app["name"] != "Development" || app["creditUnitsPerUsd"] != "1000.000000000001" || key["productUserChargeMultiplier"] != "1.000001" || router["members"].([]any)[0] != "backend" {
		t.Fatal("clone changed configuration or routing references")
	}
	if _, exists := key["cap"]; !exists || key["cap"] != nil {
		t.Fatal("explicit null was lost")
	}
	if _, exists := key["allowedModels"]; exists {
		t.Fatal("clone replaced inherited models with an explicit policy")
	}
	if _, exists := cloned.data["serviceKeys"]; exists || len(cloned.data["providerCredentials"].(map[string]any)) != 0 {
		t.Fatal("clone changed omitted or empty resource sections")
	}
	if _, err := Clone(source, destination, "Another name"); err == nil {
		t.Fatal("clone overwrote its existing destination")
	}
	if _, err := Clone(source, source, "Another name"); err == nil || !bytes.Equal(input, readFile(t, source)) {
		t.Fatal("clone did not protect its own source as a destination")
	}
}

func TestCloneRejectsSharedCredentialsWithoutCreatingADestination(t *testing.T) {
	for _, section := range []string{"providerCredentials", "serviceKeys"} {
		directory := t.TempDir()
		source := filepath.Join(directory, "source.json")
		destination := filepath.Join(directory, "clone.json")
		resource := map[string]any{"id": "shared_id", "name": "Shared resource"}
		data := map[string]any{"version": 1, "app": map[string]any{"id": "app_existing", "name": "Example", "allowedModels": []string{"model_a"}}, section: map[string]any{"shared": resource}}
		encoded, _ := json.Marshal(data)
		if err := os.WriteFile(source, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Clone(source, destination, "Development")
		assertProblem(t, err, "shared_clone_policy_required", "/"+section)
		if _, err := os.Stat(destination); !os.IsNotExist(err) {
			t.Fatal("a rejected clone created a destination")
		}
	}
}

func TestCloneRejectsAnUpdateThatLacksCreationFields(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "patch.json")
	destination := filepath.Join(directory, "clone.json")
	if err := os.WriteFile(source, []byte(`{"version":1,"app":{"id":"app_existing"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Clone(source, destination, "Development")
	assertProblem(t, err, "required_field", "/app/allowedModels")
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("an incomplete clone created a destination")
	}
}

func TestConcurrentInitAllowsOnlyOneWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zorveus.json")
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, name := range []string{"First", "Second"} {
		workers.Go(func() {
			_, err := Init(path, name, []string{"model_a"})
			results <- err
		})
	}
	workers.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if problem.Safe(err).Code != "destination_exists" {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal("more than one writer initialized the same file")
	}
	if _, err := Load(path); err != nil {
		t.Fatal("the successful writer did not leave a valid manifest")
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
