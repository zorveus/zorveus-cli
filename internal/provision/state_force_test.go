package provision

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/statestore"
)

func TestForceStateInitArchivesOnlyThisManifestAndPlansANewApp(t *testing.T) {
	service, api, _, config, plan := setup(t)
	planAndApply(t, service, config, plan)
	previous, err := os.ReadFile(statePath(config))
	if err != nil {
		t.Fatal(err)
	}
	// An abandoned local apply belongs in the backup, even if it was unfinished.
	pendingPath := filepath.Join(directory(config), "pending.apply.json")
	pending := encoded(Journal{Version: 1, Config: config})
	if err := writeNew(pendingPath, pending); err != nil {
		t.Fatal(err)
	}
	otherPath := filepath.Join(directory(config), "other.apply.json")
	other := encoded(Journal{Version: 1, Config: filepath.Join(filepath.Dir(config), "other.json")})
	if err := writeNew(otherPath, other); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(newManifest), 0600); err != nil {
		t.Fatal(err)
	}
	backend := &durableTestBackend{}
	service.StateConfig = &statestore.Config{Backend: "s3", Bucket: "state", Key: "new-app", Region: "us-east-1"}
	service.BackendFactory = func(context.Context, statestore.Config) (statestore.Backend, error) { return backend, nil }
	writes := len(api.writes)
	result, err := service.InitializeState(context.Background(), config, false, true)
	if err != nil || result.BackupPath == "" || result.AppID != "" {
		t.Fatalf("force initialization failed: %+v %v", result, err)
	}
	for name, expected := range map[string][]byte{filepath.Base(statePath(config)): previous, "pending.apply.json": pending} {
		actual, err := os.ReadFile(filepath.Join(result.BackupPath, name))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("backup did not preserve %s: %v", name, err)
		}
		if _, err := os.Lstat(filepath.Join(directory(config), name)); !os.IsNotExist(err) {
			t.Fatalf("old tracking is still active: %s", name)
		}
	}
	completed, err := filepath.Glob(filepath.Join(result.BackupPath, "*.apply.json"))
	if err != nil || len(completed) != 2 {
		t.Fatal("force did not archive both completed and unfinished applies")
	}
	if data, err := os.ReadFile(otherPath); err != nil || !bytes.Equal(data, other) {
		t.Fatal("force changed another manifest's journal")
	}
	if data, err := os.ReadFile(config); err != nil || string(data) != newManifest {
		t.Fatal("force changed the desired manifest")
	}
	if len(api.writes) != writes {
		t.Fatal("state initialization changed Zorveus resources")
	}
	proposed, err := service.Plan(context.Background(), "", config, plan)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range proposed.Changes {
		if change.Kind == "create_app" {
			found = true
		}
	}
	if !found || len(api.writes) != writes {
		t.Fatal("force did not leave a new app ready for review without API mutations")
	}
}

func TestForceStateInitRestoresLocalTrackingWhenBackendRejectsInitialization(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "storage failure"
		if existing {
			name = "existing remote object"
		}
		t.Run(name, func(t *testing.T) {
			service, _, _, config, plan := setup(t)
			planAndApply(t, service, config, plan)
			entries, err := os.ReadDir(directory(config))
			if err != nil {
				t.Fatal(err)
			}
			before := map[string][]byte{}
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(directory(config), entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				before[entry.Name()] = data
			}
			backend := &durableTestBackend{}
			if existing {
				if _, err := statestore.Initialize(context.Background(), backend, encoded(map[string]string{"existing": "deployment"})); err != nil {
					t.Fatal(err)
				}
			} else {
				backend.fail = func(statestore.Document) bool { return true }
			}
			remoteBefore := bytes.Clone(backend.data)
			service.StateConfig = &statestore.Config{Backend: "s3", Bucket: "state", Key: "prod", Region: "us-east-1"}
			service.BackendFactory = func(context.Context, statestore.Config) (statestore.Backend, error) { return backend, nil }
			if _, err := service.InitializeState(context.Background(), config, false, true); err == nil {
				t.Fatal("force accepted failed backend initialization")
			}
			if !bytes.Equal(remoteBefore, backend.data) {
				t.Fatal("force overwrote durable state")
			}
			for name, expected := range before {
				actual, err := os.ReadFile(filepath.Join(directory(config), name))
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatalf("force did not restore %s: %v", name, err)
				}
			}
		})
	}
}

func TestForceStateInitRejectsCopyLocalAndPreservesDurableBindings(t *testing.T) {
	service, _, backend, config, _ := durableSetup(t)
	before := bytes.Clone(backend.data)
	if _, err := service.InitializeState(context.Background(), config, true, true); problem.Safe(err).Code != "invalid_arguments" {
		t.Fatalf("force and copy-local were accepted together: %v", err)
	}
	if result, err := service.InitializeState(context.Background(), config, false, true); err != nil || !result.AlreadyInitialized || result.BackupPath != "" {
		t.Fatalf("force did not reuse the bound deployment: %+v %v", result, err)
	}
	if !bytes.Equal(before, backend.data) {
		t.Fatal("force changed a bound deployment")
	}
}
