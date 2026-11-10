package provision

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestFindSetupConfigurationsRequiresMatchingTargetAndAppID(t *testing.T) {
	directory := t.TempDir()
	target := Target{Origin: "https://api.example.test", Org: "org_a", User: "user_a"}
	for _, test := range []struct {
		file, appID, savedID string
		target               Target
	}{
		{"zorveus.existing.json", "app_existing", "app_existing", target},
		{"zorveus.other-org.json", "app_other", "app_other", Target{Origin: target.Origin, Org: "org_b"}},
		{"zorveus.other-api.json", "app_other", "app_other", Target{Origin: "https://other.example.test", Org: target.Org}},
		{"zorveus.wrong-id.json", "app_wrong", "app_expected", target},
	} {
		config := filepath.Join(directory, test.file)
		data := encoded(map[string]any{"version": 1, "app": map[string]any{"id": test.appID, "name": "Same name", "allowedModels": []string{"*"}}})
		if err := os.WriteFile(config, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(statePath(config)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(statePath(config), encoded(State{Version: 1, Target: test.target, AppID: test.savedID}), 0600); err != nil {
			t.Fatal(err)
		}
	}
	configurations, err := FindSetupConfigurations(filepath.Join(directory, "zorveus.json"), target)
	if err != nil || len(configurations) != 1 || configurations[0].AppID != "app_existing" || filepath.Base(configurations[0].Config) != "zorveus.existing.json" {
		t.Fatalf("ambiguous or unrelated setup was offered: %+v %v", configurations, err)
	}
}

func TestArchiveSetupStateKeepsHistoryAndPreventsOldPlanReplay(t *testing.T) {
	service, backend, _, config, planPath := setup(t)
	planAndApply(t, service, config, planPath)
	var plan Plan
	if _, _, err := loadJSON(planPath, &plan); err != nil {
		t.Fatal(err)
	}
	stateBefore, _ := os.ReadFile(statePath(config))
	journalBefore, _ := os.ReadFile(journalPath(plan))
	if err := os.WriteFile(config, []byte(newManifest), 0600); err != nil {
		t.Fatal(err)
	}
	writes := len(backend.writes)
	backup, err := ArchiveSetupState(context.Background(), config, plan.Target, "")
	if err != nil {
		t.Fatal(err)
	}
	stateAfter, _ := os.ReadFile(backup)
	journalAfter, _ := os.ReadFile(journalPath(plan))
	manifestAfter, _ := os.ReadFile(config)
	if !bytes.Equal(stateBefore, stateAfter) || !bytes.Equal(journalBefore, journalAfter) || !bytes.Equal([]byte(newManifest), manifestAfter) {
		t.Fatal("reset lost state history or changed the manifest")
	}
	if _, err := os.Stat(statePath(config)); !os.IsNotExist(err) {
		t.Fatal("old tracking still occupies the active state path")
	}
	if _, err := service.Apply(context.Background(), "", planPath, true, false); err == nil || len(backend.writes) != writes {
		t.Fatal("reset allowed an already completed creation plan to run again")
	}
}

func TestArchiveSetupStateRefusesAnUnfinishedApply(t *testing.T) {
	_, _, _, config, _ := setup(t)
	target := Target{Origin: "https://api.example.test", Org: "org_a", User: "user_a"}
	if err := os.MkdirAll(directory(config), 0700); err != nil {
		t.Fatal(err)
	}
	state := encoded(State{Version: 1, Target: target, AppID: "app_previous"})
	if err := os.WriteFile(statePath(config), state, 0600); err != nil {
		t.Fatal(err)
	}
	journal := encoded(Journal{Version: 1, Config: config, Complete: false})
	if err := os.WriteFile(filepath.Join(directory(config), "pending.apply.json"), journal, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ArchiveSetupState(context.Background(), config, target, ""); err == nil || problem.Safe(err).Code != "apply_pending" {
		t.Fatalf("unfinished apply was reset: %v", err)
	}
	after, _ := os.ReadFile(statePath(config))
	if !bytes.Equal(state, after) {
		t.Fatal("unfinished apply lost its state")
	}
}
