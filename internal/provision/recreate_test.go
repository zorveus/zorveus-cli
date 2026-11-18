package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/statestore"
)

func TestApplyDestroyApplyRecreatesInTheSameManifestAndBackend(t *testing.T) {
	for _, storage := range []string{"local", "s3"} {
		t.Run(storage, func(t *testing.T) {
			var service *Service
			var backend *fakeBackend
			var config, plan, stateID string
			if storage == "s3" {
				service, backend, _, config, plan = durableSetup(t)
				document := readDocument(t, config)
				document.set("_state", *service.StateConfig)
				writeDocument(t, config, document)
				info, err := service.InspectState(context.Background(), config)
				if err != nil {
					t.Fatal(err)
				}
				stateID = info.ID
			} else {
				service, backend, _, config, plan = setup(t)
			}
			t.Setenv("RECREATE_PROVIDER_KEY", "private-provider-input")
			document := readDocument(t, config)
			document.set("serviceKeys", map[string]any{"customers": map[string]any{"name": "Customer reads", "scopes": []string{"product_users:read"}}})
			document.set("providerCredentials", map[string]any{"openai": map[string]any{"name": "OpenAI", "provider": "openai", "secretFromEnv": "RECREATE_PROVIDER_KEY"}})
			writeDocument(t, config, document)
			planAndApply(t, service, config, plan)
			first, _, _, err := loadManifest(config)
			if err != nil {
				t.Fatal(err)
			}
			destroy, err := service.PlanDestroy(context.Background(), "", config, plan+".destroy")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Apply(context.Background(), "", destroy.Plan, true, true); err != nil {
				t.Fatal(err)
			}
			original, _ := os.ReadFile(config)
			reads, writes := len(backend.reads), len(backend.writes)
			if _, err := service.Apply(context.Background(), "", plan, true, false); err == nil || len(backend.writes) != writes {
				t.Fatal("the creation plan from before destroy was accepted as current")
			}
			preview, err := service.Plan(context.Background(), "", config, plan)
			if err != nil || preview.Operations != 7 {
				t.Fatalf("the same manifest could not plan recreation: %+v %v", preview, err)
			}
			for _, change := range preview.Changes {
				if !strings.HasPrefix(change.Kind, "create_") && change.Kind != "key_cap" || change.Destructive {
					t.Fatalf("recreation proposed an operation on a deleted resource: %+v", change)
				}
			}
			if after, err := os.ReadFile(config); err != nil || !bytes.Equal(original, after) || len(backend.writes) != writes {
				t.Fatal("planning recreation modified the manifest or resources")
			}
			if _, err := service.Apply(context.Background(), "", plan, true, false); err != nil {
				t.Fatal(err)
			}
			second, _, _, err := loadManifest(config)
			if err != nil || second.App.text("id") == first.App.text("id") || second.Keys[0].text("id") == first.Keys[0].text("id") || second.Routers[0].text("id") == first.Routers[0].text("id") || second.ServiceKeys[0].text("id") == first.ServiceKeys[0].text("id") || second.Providers[0].text("id") == first.Providers[0].text("id") {
				t.Fatalf("recreation reused a deleted resource ID: %v", err)
			}
			for _, path := range backend.reads[reads:] {
				if path == "/apps/"+first.App.text("id") {
					t.Fatal("recreation looked up the deleted app")
				}
			}
			var active State
			if _, _, err := loadJSON(statePath(config), &active); err != nil || active.Destroyed || len(active.DestroyedIDs) != 0 || active.AppID != second.App.text("id") {
				t.Fatalf("recreation did not replace the destroyed tracking: %+v %v", active, err)
			}
			if status, err := service.Status(context.Background(), "", config); err != nil || status.Destroyed || !status.InSync {
				t.Fatalf("recreated deployment did not converge: %+v %v", status, err)
			}
			if next, err := service.Plan(context.Background(), "", config, plan); err != nil || next.Operations != 0 {
				t.Fatalf("a repeated plan was not empty: %+v %v", next, err)
			}
			if storage == "s3" {
				info, err := service.InspectState(context.Background(), config)
				if err != nil || info.ID != stateID || info.Destroyed {
					t.Fatalf("recreation replaced the backend identity: %+v %v", info, err)
				}
				if !readDocument(t, config).has("_state") {
					t.Fatal("recreation removed the backend configuration")
				}
			}
			before := len(backend.writes)
			if _, err := service.Apply(context.Background(), "", destroy.Plan, true, true); err == nil || len(backend.writes) != before {
				t.Fatal("the previous destroy plan affected the recreated app")
			}
			if _, err := service.PlanDestroy(context.Background(), "", config, destroy.Plan); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Apply(context.Background(), "", destroy.Plan, true, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecreationHandlesAlreadyDestroyedDurableStateWithoutNewMetadata(t *testing.T) {
	service, _, backend, config, path := durableSetup(t)
	t.Setenv("LEGACY_PROVIDER_KEY", "private-provider-input")
	desired := readDocument(t, config)
	desired.set("serviceKeys", map[string]any{"customers": map[string]any{"name": "Customer reads", "scopes": []string{"product_users:read"}}})
	desired.set("providerCredentials", map[string]any{"openai": map[string]any{"name": "OpenAI", "provider": "openai", "secretFromEnv": "LEGACY_PROVIDER_KEY"}})
	writeDocument(t, config, desired)
	planAndApply(t, service, config, path)
	preview, err := service.PlanDestroy(context.Background(), "", config, path+".destroy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), "", preview.Plan, true, true); err != nil {
		t.Fatal(err)
	}
	var document statestore.Document
	_ = json.Unmarshal(backend.data, &document)
	var snapshot durableSnapshot
	_ = json.Unmarshal(document.Data, &snapshot)
	snapshot.State.DestroyedIDs = nil
	document.Data = encoded(snapshot)
	backend.data = encoded(document)
	if result, err := service.Plan(context.Background(), "", config, path); err != nil || result.Operations != 7 {
		t.Fatalf("an already destroyed project still required a new manifest: %+v %v", result, err)
	}
}

func TestRecreationOnlyClearsConfirmedDeletedIDs(t *testing.T) {
	data := []byte(`{"version":1,"app":{"id":"app_old","name":"Same project","allowedModels":["*"]},"serviceKeys":{"removed":{"id":"service_old","name":"Replacement","scopes":["product_users:read"]},"existing":{"id":"service_active"}}}`)
	state := State{Destroyed: true, AppID: "app_old", Target: Target{Org: "org_a"}}
	resolved, err := recreationManifest(data, state, []string{"app_old", "service_old"})
	if err != nil {
		t.Fatal(err)
	}
	var desired Manifest
	if err := json.Unmarshal(resolved, &desired); err != nil {
		t.Fatal(err)
	}
	if desired.App.text("id") != "" || desired.App.text("name") != "Same project" {
		t.Fatal("recreation did not preserve app settings")
	}
	for index, resource := range desired.ServiceKeys {
		name := desired.resourceNames["serviceKeys"][index]
		if name == "existing" && resource.text("id") != "service_active" || name == "removed" && resource.text("id") != "" {
			t.Fatal("recreation discarded an unrelated existing resource ID")
		}
	}
	_, err = recreationManifest([]byte(`{"version":1,"app":{"id":"app_old"}}`), state, []string{"app_old"})
	if problem.Safe(err).Code != "recreation_settings_required" {
		t.Fatalf("an ID-only manifest did not explain its missing creation settings: %v", err)
	}
}

func TestRecreationFinalizationCanResumeWithOldLocalIDs(t *testing.T) {
	service, backend, remoteState, config, plan := durableSetup(t)
	planAndApply(t, service, config, plan)
	preview, err := service.PlanDestroy(context.Background(), "", config, plan+".destroy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), "", preview.Plan, true, true); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(config)
	previousState, _ := os.ReadFile(statePath(config))
	if _, err := service.Plan(context.Background(), "", config, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), "", plan, true, false); err != nil {
		t.Fatal(err)
	}
	finalManifest, _ := os.ReadFile(config)
	var document statestore.Document
	_ = json.Unmarshal(remoteState.data, &document)
	var snapshot durableSnapshot
	_ = json.Unmarshal(document.Data, &snapshot)
	// The final checkpoint reached S3, but the process stopped before replacing local files.
	snapshot.Operation.Journal.Complete = false
	document.Data = encoded(snapshot)
	remoteState.data = encoded(document)
	if err := os.WriteFile(config, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath(config), previousState, 0600); err != nil {
		t.Fatal(err)
	}
	writes := len(backend.writes)
	var unrelated State
	_ = json.Unmarshal(previousState, &unrelated)
	unrelated.AppID = "app_unrelated"
	if err := os.WriteFile(statePath(config), encoded(unrelated), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(context.Background(), "", config, true, false); problem.Safe(err).Code != "state_backend_mismatch" || len(backend.writes) != writes {
		t.Fatalf("finalization replaced unrelated local tracking: %v", err)
	}
	if err := os.WriteFile(statePath(config), previousState, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(context.Background(), "", config, true, false); err != nil || len(backend.writes) != writes {
		t.Fatalf("finalization could not recover the new IDs: %v", err)
	}
	if after, err := os.ReadFile(config); err != nil || !bytes.Equal(after, finalManifest) {
		t.Fatal("recovery did not write the recreated manifest")
	}
	// The manifest can also be replaced before the process saves its local state file.
	_ = json.Unmarshal(remoteState.data, &document)
	_ = json.Unmarshal(document.Data, &snapshot)
	snapshot.Operation.Journal.Complete = false
	document.Data = encoded(snapshot)
	remoteState.data = encoded(document)
	if err := os.WriteFile(statePath(config), previousState, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(context.Background(), "", config, true, false); err != nil || len(backend.writes) != writes {
		t.Fatalf("recovery failed after only the manifest was updated: %v", err)
	}
	second, fresh, _ := newRunner(t, service, finalManifest)
	if _, err := second.InitializeState(context.Background(), fresh, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory(fresh), filepath.Base(fresh)+".backend.json")); err != nil {
		t.Fatal("the existing backend could not be reused after recreation")
	}
}
