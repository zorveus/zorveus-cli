package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/statestore"
)

func TestDestroyPreviewsThenRemovesAppAndEveryListedCredential(t *testing.T) {
	service, backend, _, config, path := setup(t)
	t.Setenv("DESTROY_PROVIDER_KEY", "private-provider-input")
	document := readDocument(t, config)
	document.set("serviceKeys", map[string]any{"customers": map[string]any{"name": "Customer access", "scopes": []string{"product_users:read"}}})
	document.set("providerCredentials", map[string]any{"openai": map[string]any{"name": "OpenAI", "provider": "openai", "secretFromEnv": "DESTROY_PROVIDER_KEY"}})
	writeDocument(t, config, document)
	planAndApply(t, service, config, path)
	t.Setenv("DESTROY_PROVIDER_KEY", "")
	backend.services["unrelated_service"] = fields(map[string]string{"org_service_api_key_id": "unrelated_service", "org_id": "org_a", "status": "active"})
	backend.providers["unrelated_provider"] = fields(map[string]string{"provider_credential_id": "unrelated_provider", "org_id": "org_a", "status": "active"})
	// This connection belongs to the app but was created outside this manifest.
	outside := fields(backend.connections["conn_1"])
	outside.set("app_connection_id", "conn_outside")
	var key Fields
	_ = json.Unmarshal(outside["active_key"], &key)
	key.set("app_connection_id", "conn_outside")
	key.set("api_key_id", "outside_key")
	outside.set("active_key", key)
	backend.connections["conn_outside"] = outside
	beforeManifest, _ := os.ReadFile(config)
	writes := len(backend.writes)
	preview, err := service.PlanDestroy(context.Background(), "", config, path+".destroy")
	if err != nil || preview.Operations != 8 || !preview.Destroy || preview.Target.Org != "org_a" {
		t.Fatalf("incomplete destroy preview: %+v %v", preview, err)
	}
	if len(backend.writes) != writes {
		t.Fatal("destroy preview changed resources")
	}
	if _, err := service.Apply(context.Background(), "", preview.Plan, false, true); problem.Safe(err).Code != "acceptance_required" {
		t.Fatalf("destroy did not require confirmation: %v", err)
	}
	if _, err := service.Apply(context.Background(), "", preview.Plan, true, false); problem.Safe(err).Code != "destructive_acceptance_required" {
		t.Fatalf("saved destroy plan bypassed apply's deletion approval: %v", err)
	}
	result, err := service.Apply(context.Background(), "", preview.Plan, true, true)
	if err != nil || !result.Destroyed {
		t.Fatalf("destroy failed: %+v %v", result, err)
	}
	if backend.app.text("status") != "deleted" || backend.services["service_1"].text("status") != "revoked" || backend.providers["provider_1"].text("status") != "deleted" || len(backend.caps) != 0 {
		t.Fatal("destroy left active app resources or listed credentials")
	}
	for _, connection := range backend.connections {
		if connection.text("status") != "revoked" {
			t.Fatal("destroy left an active inference connection")
		}
	}
	if backend.routers["router_1"].text("status") != "revoked" || backend.services["unrelated_service"].text("status") != "active" || backend.providers["unrelated_provider"].text("status") != "active" {
		t.Fatal("destroy missed routing or swept unrelated organization credentials")
	}
	if backend.writes[len(backend.writes)-1] != "DELETE /apps/app_a" {
		t.Fatal("app deletion must follow all child cleanup")
	}
	afterManifest, _ := os.ReadFile(config)
	if !bytes.Equal(beforeManifest, afterManifest) {
		t.Fatal("destroy rewrote the manifest instead of retaining the deleted IDs")
	}
	var state State
	if _, _, err := loadJSON(statePath(config), &state); err != nil || !state.Destroyed {
		t.Fatalf("destroy completion was not recorded: %v", err)
	}
	writes = len(backend.writes)
	if again, err := service.PlanDestroy(context.Background(), "", config, path+".destroy"); err != nil || again.Operations != 0 || len(backend.writes) != writes {
		t.Fatalf("repeated destroy was not a no-op: %+v %v", again, err)
	}
	t.Setenv("DESTROY_PROVIDER_KEY", "private-provider-input")
	if plan, err := service.Plan(context.Background(), "", config, path); err != nil || plan.Operations != 7 {
		t.Fatalf("normal plan could not recreate the destroyed deployment: %+v %v", plan, err)
	}
	if status, err := service.Status(context.Background(), "", config); err != nil || !status.Destroyed || status.InSync {
		t.Fatalf("status did not explain the destroyed deployment: %+v %v", status, err)
	}
}

func TestDestroyPreflightsScopesAndRejectsManifestDriftBeforeWriting(t *testing.T) {
	for _, reason := range []string{"missing scope", "manifest changed"} {
		t.Run(reason, func(t *testing.T) {
			service, backend, store, config, path := setup(t)
			planAndApply(t, service, config, path)
			preview, err := service.PlanDestroy(context.Background(), "", config, path+".destroy")
			if err != nil {
				t.Fatal(err)
			}
			if reason == "missing scope" {
				store.login.Scope = strings.ReplaceAll(store.login.Scope, "apps:delete", "")
			} else {
				desired := readDocument(t, config)
				desired.set("organizationId", "org_different")
				writeDocument(t, config, desired)
			}
			before := len(backend.writes)
			if _, err := service.Apply(context.Background(), "", preview.Plan, true, true); err == nil || len(backend.writes) != before {
				t.Fatalf("destroy wrote before validating %s: %v", reason, err)
			}
		})
	}
}

func TestDestroyResumesFromDurableStateOnAnotherRunner(t *testing.T) {
	service, backend, state, config, path := durableSetup(t)
	planAndApply(t, service, config, path)
	manifest, _ := os.ReadFile(config)
	preview, err := service.PlanDestroy(context.Background(), "", config, path+".destroy")
	if err != nil {
		t.Fatal(err)
	}
	backend.failDestroyPath = "/app-connections/conn_1/revoke"
	if _, err := service.Apply(context.Background(), "", preview.Plan, true, true); err == nil {
		t.Fatal("expected an interrupted destroy")
	}
	second, fresh, _ := newRunner(t, service, manifest)
	if _, err := second.Resume(context.Background(), "", fresh, true, true); err != nil {
		t.Fatal(err)
	}
	routerWrites := 0
	for _, write := range backend.writes {
		if write == "POST /orgs/org_a/apps/app_a/routing-keys/router_1/revoke" {
			routerWrites++
		}
	}
	if routerWrites != 1 || backend.app.text("status") != "deleted" {
		t.Fatal("resuming destroy repeated completed writes or missed the app")
	}
	info, err := second.InspectState(context.Background(), fresh)
	if err != nil || !info.Destroyed || !info.ApplyComplete || info.Lock != nil || bytes.Contains(state.data, []byte("private-")) {
		t.Fatalf("destroy state was not safely completed: %+v %v", info, err)
	}
}

func TestDestroyStorageFailureStopsBeforeSendingMutation(t *testing.T) {
	service, backend, state, config, path := durableSetup(t)
	planAndApply(t, service, config, path)
	preview, err := service.PlanDestroy(context.Background(), "", config, path+".destroy")
	if err != nil {
		t.Fatal(err)
	}
	state.fail = func(document statestore.Document) bool {
		var snapshot durableSnapshot
		_ = json.Unmarshal(document.Data, &snapshot)
		return snapshot.Operation != nil && snapshot.Operation.Plan.Destroy && slices.ContainsFunc(snapshot.Operation.Journal.Steps, func(progress Progress) bool { return progress.Started })
	}
	before := len(backend.writes)
	if _, err := service.Apply(context.Background(), "", preview.Plan, true, true); err == nil || len(backend.writes) != before {
		t.Fatalf("destroy sent a mutation after losing its checkpoint: %v", err)
	}
}

func TestDestroyRerunUsesItsPendingLocalPlan(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	preview, err := service.PlanDestroy(context.Background(), "", config, path+".destroy")
	if err != nil {
		t.Fatal(err)
	}
	var original Plan
	if _, _, err := loadJSON(preview.Plan, &original); err != nil {
		t.Fatal(err)
	}
	backend.failDestroyPath = "/caps/cap_conn_1"
	if _, err := service.Apply(context.Background(), "", preview.Plan, true, true); err == nil {
		t.Fatal("expected a failed cap removal")
	}
	resumed, err := service.PlanDestroy(context.Background(), "", config, preview.Plan)
	if err != nil || !resumed.Resuming {
		t.Fatal(err)
	}
	var restored Plan
	if _, _, err := loadJSON(resumed.Plan, &restored); err != nil || restored.ID != original.ID {
		t.Fatal("rerunning destroy replaced its unfinished plan")
	}
	if _, err := service.Apply(context.Background(), "", resumed.Plan, true, true); err != nil {
		t.Fatal(err)
	}
	routerWrites := 0
	for _, write := range backend.writes {
		if write == "POST /orgs/org_a/apps/app_a/routing-keys/router_1/revoke" {
			routerWrites++
		}
	}
	if routerWrites != 1 || backend.app.text("status") != "deleted" {
		t.Fatal("rerunning destroy repeated completed work or missed the app")
	}
}

func TestDestroyDoesNotHideUncertainAppDeletionAsSuccess(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	preview, err := service.PlanDestroy(context.Background(), "", config, path+".destroy")
	if err != nil {
		t.Fatal(err)
	}
	backend.loseDestroyResponsePath = "/apps/app_a"
	if _, err := service.Apply(context.Background(), "", preview.Plan, true, true); err == nil {
		t.Fatal("destroy reported success without app deletion confirmation")
	}
	if _, err := service.Apply(context.Background(), "", preview.Plan, true, true); problem.Safe(err).Code != "destroy_app_outcome_unknown" {
		t.Fatalf("destroy did not explain the uncertain app deletion: %v", err)
	}
	var state State
	if _, _, err := loadJSON(statePath(config), &state); err != nil || state.Destroyed {
		t.Fatal("destroy discarded recovery state for an uncertain app deletion")
	}
}

func TestDestroyPreservesManifestEditsAndCanFinishAfterTheyAreRestored(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	original, _ := os.ReadFile(config)
	preview, err := service.PlanDestroy(context.Background(), "", config, path+".destroy")
	if err != nil {
		t.Fatal(err)
	}
	backend.changeConfig = config
	if _, err := service.Apply(context.Background(), "", preview.Plan, true, true); problem.Safe(err).Code != "config_changed" {
		t.Fatalf("destroy overwrote edits made during cleanup: %v", err)
	}
	edited, _ := os.ReadFile(config)
	if !bytes.Contains(edited, []byte("User edit")) {
		t.Fatal("destroy lost the user's manifest edits")
	}
	if err := os.WriteFile(config, original, 0600); err != nil {
		t.Fatal(err)
	}
	writes := len(backend.writes)
	if _, err := service.Apply(context.Background(), "", preview.Plan, true, true); err != nil || len(backend.writes) != writes {
		t.Fatalf("finishing destroy repeated mutations: %v", err)
	}
}
