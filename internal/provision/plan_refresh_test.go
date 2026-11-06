package provision

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestReplanningReplacesTheSavedPlanWithCurrentChanges(t *testing.T) {
	service, backend, _, config, path := setup(t)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	var first Plan
	if _, _, err := loadJSON(path, &first); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(newManifest, `"name":"Demo"`, `"name":"Updated app"`, 1)
	if err := os.WriteFile(config, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := service.Plan(context.Background(), "", config, path)
	if err != nil {
		t.Fatal(err)
	}
	var refreshed Plan
	if exists, _, err := loadJSON(path, &refreshed); err != nil || !exists {
		t.Fatalf("refreshed plan missing: %v", err)
	}
	if refreshed.ID == first.ID || refreshed.Steps[0].After.text("app_name") != "Updated app" || result.Plan != path {
		t.Fatal("replanning did not replace the previous plan with the current configuration")
	}
	current, err := os.ReadFile(config)
	if err != nil || string(current) != edited || len(backend.writes) != 0 {
		t.Fatal("replanning changed the manifest or wrote to the backend")
	}
}

func TestFailedReplanningPreservesThePreviousPlan(t *testing.T) {
	service, _, store, config, path := setup(t)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store.login.ExpiresAt = time.Now().Add(-time.Minute)
	if _, err := service.Plan(context.Background(), "", config, path); err == nil {
		t.Fatal("replanning unexpectedly succeeded with an expired login")
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != string(original) {
		t.Fatal("failed replanning damaged the previous plan")
	}
}

func TestPlanOutputDoesNotOverwriteManifestsOrLocalState(t *testing.T) {
	for _, destination := range []string{"manifest", "state", "another file", "another manifest's plan"} {
		t.Run(destination, func(t *testing.T) {
			service, _, _, config, path := setup(t)
			output := path
			var original []byte
			switch destination {
			case "manifest":
				output = config
				original = []byte(newManifest)
			case "state":
				output = statePath(config)
			case "another file":
				original = []byte(`{"important":"keep this"}`)
			case "another manifest's plan":
				if _, err := service.Plan(context.Background(), "", config, output); err != nil {
					t.Fatal(err)
				}
				var other Plan
				_, _, _ = loadJSON(output, &other)
				other.Config = filepath.Join(filepath.Dir(config), "other.json")
				original, _ = json.Marshal(other)
			}
			if original != nil {
				if err := os.WriteFile(output, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := service.Plan(context.Background(), "", config, output)
			if err == nil || problem.Safe(err).Code != "invalid_plan_output" {
				t.Fatalf("unsafe output path accepted: %v", err)
			}
			current, err := os.ReadFile(output)
			if original == nil {
				if !os.IsNotExist(err) {
					t.Fatal("planning wrote into the local state directory")
				}
			} else if err != nil || string(current) != string(original) {
				t.Fatal("planning overwrote an unrelated file")
			}
		})
	}
}
