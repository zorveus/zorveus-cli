package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/statestore"
)

type competingStateInitializer struct {
	*durableTestBackend
}

func (backend *competingStateInitializer) Write(ctx context.Context, data []byte, version string) (string, error) {
	var document statestore.Document
	if err := json.Unmarshal(data, &document); err != nil {
		return "", err
	}
	document.ID = "concurrently-initialized-state"
	if _, err := backend.durableTestBackend.Write(ctx, encoded(document), version); err != nil {
		return "", err
	}
	return "", statestore.Conflict()
}

func TestStateInitReusesStateCreatedByAnotherCheckoutDuringInitialization(t *testing.T) {
	service, _, _, config, _ := setup(t)
	backend := &competingStateInitializer{durableTestBackend: &durableTestBackend{}}
	service.StateConfig = &statestore.Config{Backend: "s3", Bucket: "state", Key: "shared", Region: "us-east-1"}
	service.BackendFactory = func(context.Context, statestore.Config) (statestore.Backend, error) { return backend, nil }
	result, err := service.InitializeState(context.Background(), config, false, false)
	if err != nil || !result.AlreadyInitialized || result.ID != "concurrently-initialized-state" || backend.version != 1 {
		t.Fatalf("init failed to reuse the concurrent initializer's state: %+v %v", result, err)
	}
}

func TestRepeatedStateInitPreservesAppliedStateAndActiveLock(t *testing.T) {
	service, api, backend, config, plan := durableSetup(t)
	planAndApply(t, service, config, plan)
	session, err := statestore.Acquire(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	before := bytes.Clone(backend.data)
	version, writes := backend.version, len(api.writes)
	for _, options := range []struct{ copyLocal, force bool }{{}, {copyLocal: true}, {force: true}} {
		result, err := service.InitializeState(context.Background(), config, options.copyLocal, options.force)
		if err != nil || !result.AlreadyInitialized || result.ID != session.Document.ID || result.Revision != session.Document.Revision || result.AppID != "app_a" || !result.ApplyComplete || result.BackupPath != "" {
			t.Fatalf("repeated init changed the deployment: %+v %v", result, err)
		}
		if !bytes.Equal(before, backend.data) || backend.version != version || len(api.writes) != writes {
			t.Fatal("repeated init wrote remote state or changed Zorveus resources")
		}
	}
	second, fresh, _ := newRunner(t, service, []byte(newManifest))
	result, err := second.InitializeState(context.Background(), fresh, false, false)
	if err != nil || !result.AlreadyInitialized || result.Lock == nil || result.Lock.ID != session.Document.Lock.ID {
		t.Fatalf("fresh checkout did not reconnect: %+v %v", result, err)
	}
	var restored State
	if exists, _, err := loadJSON(statePath(fresh), &restored); err != nil || !exists || restored.AppID != "app_a" {
		t.Fatalf("fresh checkout did not restore its local tracking: %+v %v", restored, err)
	}
	if !bytes.Equal(before, backend.data) || backend.version != version || len(api.writes) != writes {
		t.Fatal("reconnecting a checkout changed shared state or its lock")
	}
}

func TestStateInitDoesNotReplaceMissingBoundState(t *testing.T) {
	service, _, backend, config, _ := durableSetup(t)
	receipt := filepath.Join(directory(config), filepath.Base(config)+".backend.json")
	before, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	backend.data, backend.version = nil, 0
	for _, force := range []bool{false, true} {
		if _, err := service.InitializeState(context.Background(), config, false, force); problem.Safe(err).Code != "remote_state_missing" {
			t.Fatalf("missing bound state was treated as a new deployment: %v", err)
		}
		if backend.version != 0 || len(backend.data) != 0 {
			t.Fatal("init replaced missing bound state")
		}
	}
	if after, err := os.ReadFile(receipt); err != nil || !bytes.Equal(before, after) {
		t.Fatal("init discarded the previous state binding")
	}
}

func TestRepeatedStateInitRejectsConflictingResourceAndStateIDs(t *testing.T) {
	for _, conflict := range []string{"resource", "state", "backend"} {
		t.Run(conflict, func(t *testing.T) {
			service, _, backend, config, plan := durableSetup(t)
			planAndApply(t, service, config, plan)
			switch conflict {
			case "resource":
				desired := readDocument(t, config)
				var app Fields
				_ = json.Unmarshal(desired["app"], &app)
				app.set("id", "app_different")
				desired.set("app", app)
				writeDocument(t, config, desired)
			case "state":
				var document statestore.Document
				_ = json.Unmarshal(backend.data, &document)
				document.ID = "replacement-state"
				backend.data = encoded(document)
			case "backend":
				selected := *service.StateConfig
				selected.Key = "different-location"
				service.StateConfig = &selected
			}
			before, version := bytes.Clone(backend.data), backend.version
			if _, err := service.InitializeState(context.Background(), config, false, true); err == nil {
				t.Fatal("init accepted a conflicting deployment")
			}
			if !bytes.Equal(before, backend.data) || version != backend.version {
				t.Fatal("init modified conflicting durable state")
			}
		})
	}
}

func TestStateInitDoesNotTreatReadFailureAsMissingState(t *testing.T) {
	service, _, backend, config, _ := durableSetup(t)
	backend.readErr = statestore.Unavailable()
	before, version := bytes.Clone(backend.data), backend.version
	if _, err := service.InitializeState(context.Background(), config, false, true); problem.Safe(err).Code != "remote_state_unavailable" {
		t.Fatalf("init ignored a storage access failure: %v", err)
	}
	if !bytes.Equal(before, backend.data) || version != backend.version {
		t.Fatal("init wrote state after a failed read")
	}
}
