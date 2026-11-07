package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

const namedManifest = `{
  "version": 1,
  "app": {
    "name": "Demo",
    "allowedModels": [
      "*"
    ]
  },
  "keys": {
    "primary": {
      "name": "Primary",
      "cap": {
        "amount": "100.000000000001",
        "currency": "USD",
        "period": "monthly"
      }
    },
    "backup": {
      "name": "Backup"
    }
  },
  "routingKeys": {
    "public": {
      "name": "Router",
      "members": [
        "primary",
        "backup"
      ],
      "defaultSelector": "primary"
    }
  }
}`

func TestNamedResourcesCreateWriteIDsAndConverge(t *testing.T) {
	service, backend, _, config, planPath := setup(t)
	t.Setenv("NAMED_PROVIDER_KEY", "private-provider-key")
	var document Fields
	if err := json.Unmarshal([]byte(namedManifest), &document); err != nil {
		t.Fatal(err)
	}
	document.set("serviceKeys", map[string]any{"billing": map[string]any{"name": "Billing", "scopes": []string{"product_users:read"}}})
	document.set("providerCredentials", map[string]any{"openai": map[string]any{"name": "OpenAI", "provider": "openai", "secretFromEnv": "NAMED_PROVIDER_KEY"}})
	writeDocument(t, config, document)
	planAndApply(t, service, config, planPath)
	var saved struct {
		Version   int               `json:"version"`
		Keys      map[string]Fields `json:"keys"`
		Routers   map[string]Fields `json:"routingKeys"`
		Services  map[string]Fields `json:"serviceKeys"`
		Providers map[string]Fields `json:"providerCredentials"`
	}
	data, _ := os.ReadFile(config)
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 || saved.Keys["backup"].text("id") != "conn_1" || saved.Keys["primary"].text("id") != "conn_2" || saved.Routers["public"].text("id") != "router_1" || saved.Services["billing"].text("id") != "service_1" || saved.Providers["openai"].text("id") != "provider_1" {
		t.Fatal("IDs were not recorded under their stable object names")
	}
	if saved.Keys["primary"].has("routingAlias") || saved.Routers["public"].has("alias") {
		t.Fatal("object manifests contain redundant alias fields")
	}
	status, err := service.Status(context.Background(), "", config)
	if err != nil || !status.InSync {
		t.Fatalf("named status did not converge: %+v %v", status, err)
	}
	refs := map[string]bool{}
	for _, resource := range status.Resources {
		refs[resource.Ref] = true
	}
	if !refs["router:public"] || !refs["service:billing"] || !refs["provider:openai"] {
		t.Fatal("status used array indices instead of resource names")
	}
	plan, err := service.Plan(context.Background(), "", config, planPath)
	if err != nil || plan.Operations != 0 {
		t.Fatalf("named resources did not converge: %+v %v", plan.Changes, err)
	}
	if backend.caps["conn_2"].text("amount") != "100.000000000001" {
		t.Fatal("named resource parsing changed decimal precision")
	}
}

func TestNamedPlanFromPreservesFilesUntilApplyAndKeepsTargetIDs(t *testing.T) {
	service, backend, _, target, planPath := setup(t)
	writeDocument(t, target, fields(map[string]any{
		"version": 1,
		"app":     map[string]any{"name": "Demo", "allowedModels": []string{"*"}},
		"keys": map[string]any{
			"primary": map[string]any{
				"name": "Primary",
				"cap":  map[string]string{"amount": "10", "currency": "USD", "period": "monthly"},
			},
		},
		"routingKeys": map[string]any{
			"public": map[string]any{"name": "Router", "members": []string{"primary"}, "defaultSelector": "primary"},
		},
	}))
	planAndApply(t, service, target, planPath)
	source := filepath.Join(filepath.Dir(target), "dev.json")
	writeDocument(t, source, fields(map[string]any{
		"version": 1,
		"app":     map[string]any{"id": "app_dev", "name": "Updated", "allowedModels": []string{"*"}},
		"keys": map[string]any{
			"primary": map[string]any{
				"id": "dev_primary", "name": "Primary",
				"cap": map[string]string{"amount": "200", "currency": "USD", "period": "monthly"},
			},
			"worker": map[string]string{"id": "dev_worker", "name": "Worker"},
		},
		"routingKeys": map[string]any{
			"public": map[string]any{
				"id": "dev_router", "name": "Router",
				"members": []string{"primary", "worker"}, "defaultSelector": "primary",
			},
		},
	}))
	targetBefore, _ := os.ReadFile(target)
	sourceBefore, _ := os.ReadFile(source)
	writesBefore := len(backend.writes)
	plan, err := service.PlanFrom(context.Background(), "", target, planPath, source, false)
	if err != nil || plan.Operations != 4 {
		t.Fatalf("expected app rename, cap update, worker creation and router update: %+v %v", plan.Changes, err)
	}
	targetAfterPlan, _ := os.ReadFile(target)
	if !bytes.Equal(targetBefore, targetAfterPlan) || len(backend.writes) != writesBefore {
		t.Fatal("plan --from changed the target file or remote resources")
	}
	if _, err := service.Apply(context.Background(), "", planPath, true, false); err != nil {
		t.Fatal(err)
	}
	desired, _, _, err := loadManifest(target)
	if err != nil {
		t.Fatal(err)
	}
	if desired.App.text("id") != "app_a" || desired.Keys[0].text("id") != "conn_1" || desired.Keys[1].text("id") != "conn_2" || desired.Routers[0].text("id") != "router_1" {
		t.Fatal("plan --from lost target identities")
	}
	if desired.App.text("name") != "Updated" || backend.caps["conn_1"].text("amount") != "200" {
		t.Fatal("source name or cap was not applied")
	}
	sourceAfter, _ := os.ReadFile(source)
	if !bytes.Equal(sourceBefore, sourceAfter) {
		t.Fatal("apply changed the source file")
	}
	plan, err = service.PlanFrom(context.Background(), "", target, planPath, source, false)
	if err != nil || plan.Operations != 0 {
		t.Fatalf("repeated cross-manifest plan did not converge: %+v %v", plan.Changes, err)
	}
}

func TestCrossAppPlanningKeepsNamesOnlyWhenRequested(t *testing.T) {
	service, backend, _, target, planPath := setup(t)
	writeDocument(t, target, fields(map[string]any{
		"version": 1,
		"app":     map[string]any{"name": "Subscription assistant prod", "allowedModels": []string{"*"}},
		"keys": map[string]any{
			"free": map[string]string{"name": "Free plan prod"},
			"pro":  map[string]string{"name": "Pro plan prod"},
		},
		"routingKeys": map[string]any{
			"primary": map[string]any{"name": "Subscription backend prod", "members": []string{"free", "pro"}, "defaultSelector": nil},
		},
	}))
	planAndApply(t, service, target, planPath)
	source := filepath.Join(filepath.Dir(target), "dev.json")
	document := readDocument(t, target)
	document.set("app", map[string]any{"id": "app_dev", "name": "Subscription assistant dev", "allowedModels": []string{"*"}})
	document.set("keys", map[string]any{
		"free": map[string]string{"id": "dev_free", "name": "Free plan"},
		"pro":  map[string]string{"id": "dev_pro", "name": "Pro plan"},
	})
	document.set("routingKeys", map[string]any{
		"primary": map[string]any{"id": "dev_router", "name": "Subscription backend", "members": []string{"free", "pro"}, "defaultSelector": nil},
	})
	writeDocument(t, source, document)
	targetBefore, _ := os.ReadFile(target)
	sourceBefore, _ := os.ReadFile(source)
	writesBefore := len(backend.writes)
	plan, err := service.PlanFrom(context.Background(), "", target, planPath, source, true)
	if err != nil || plan.Operations != 0 {
		t.Fatalf("environment display names produced changes: %+v %v", plan.Changes, err)
	}
	if _, err := service.Apply(context.Background(), "", planPath, true, false); err != nil {
		t.Fatal(err)
	}
	targetAfter, _ := os.ReadFile(target)
	sourceAfter, _ := os.ReadFile(source)
	if !bytes.Equal(targetBefore, targetAfter) || !bytes.Equal(sourceBefore, sourceAfter) || len(backend.writes) != writesBefore {
		t.Fatal("a name-only cross-app plan changed files or remote resources")
	}
	// Editing the target explicitly still requests a normal display-name update.
	document = readDocument(t, target)
	var app Fields
	if err := json.Unmarshal(document["app"], &app); err != nil {
		t.Fatal(err)
	}
	app.set("name", "Subscription assistant production")
	document.set("app", app)
	writeDocument(t, target, document)
	plan, err = service.Plan(context.Background(), "", target, planPath)
	if err != nil || plan.Operations != 1 || plan.Changes[0].Kind != "app_profile" {
		t.Fatalf("direct target rename did not produce a name update: %+v %v", plan.Changes, err)
	}
	if _, err := service.Apply(context.Background(), "", planPath, true, false); err != nil {
		t.Fatal(err)
	}
	if backend.app.text("app_name") != "Subscription assistant production" {
		t.Fatal("direct target rename was not applied")
	}
	// Without --keep-names, all supplied source names request updates.
	plan, err = service.PlanFrom(context.Background(), "", target, planPath, source, false)
	if err != nil || plan.Operations != 4 {
		t.Fatalf("expected four source name updates: %+v %v", plan.Changes, err)
	}
	if _, err := service.Apply(context.Background(), "", planPath, true, false); err != nil {
		t.Fatal(err)
	}
	desired, _, _, err := loadManifest(target)
	if err != nil {
		t.Fatal(err)
	}
	if desired.App.text("name") != "Subscription assistant dev" || desired.Keys[0].text("name") != "Free plan" || desired.Keys[1].text("name") != "Pro plan" || desired.Routers[0].text("name") != "Subscription backend" {
		t.Fatal("default cross-app apply did not copy source display names")
	}
	if desired.App.text("id") != "app_a" || desired.Keys[0].text("id") != "conn_1" || desired.Keys[1].text("id") != "conn_2" || desired.Routers[0].text("id") != "router_1" {
		t.Fatal("renaming resources changed production IDs")
	}
	plan, err = service.PlanFrom(context.Background(), "", target, planPath, source, false)
	if err != nil || plan.Operations != 0 {
		t.Fatalf("source name updates did not converge: %+v %v", plan.Changes, err)
	}
}

func TestNamedPlanFromRejectsChangedSourceBeforeWrites(t *testing.T) {
	service, backend, _, target, planPath := setup(t)
	if err := os.WriteFile(target, []byte(namedManifest), 0600); err != nil {
		t.Fatal(err)
	}
	planAndApply(t, service, target, planPath)
	source := filepath.Join(filepath.Dir(target), "dev.json")
	writeDocument(t, source, fields(map[string]any{"version": 1, "app": map[string]string{"id": "app_dev", "name": "Updated"}}))
	if _, err := service.PlanFrom(context.Background(), "", target, planPath, source, true); err != nil {
		t.Fatal(err)
	}
	document := readDocument(t, source)
	document.set("app", map[string]string{"id": "app_dev", "name": "Another update"})
	writeDocument(t, source, document)
	before := len(backend.writes)
	if _, err := service.Apply(context.Background(), "", planPath, true, false); err == nil || problem.Safe(err).ExitCode != 4 {
		t.Fatalf("apply accepted a changed source: %v", err)
	}
	if len(backend.writes) != before {
		t.Fatal("source conflict was detected after writes")
	}
}

func TestNamedPlanFromResumesSavedConfigurationAfterSecretSaveFailure(t *testing.T) {
	service, backend, _, target, planPath := setup(t)
	if err := os.WriteFile(target, []byte(namedManifest), 0600); err != nil {
		t.Fatal(err)
	}
	planAndApply(t, service, target, planPath)
	source := filepath.Join(filepath.Dir(target), "dev.json")
	document := readDocument(t, target)
	var keys map[string]Fields
	if err := json.Unmarshal(document["keys"], &keys); err != nil {
		t.Fatal(err)
	}
	keys["worker"] = fields(map[string]string{"id": "dev_worker", "name": "Worker"})
	document.set("keys", keys)
	writeDocument(t, source, document)
	if _, err := service.PlanFrom(context.Background(), "", target, planPath, source, true); err != nil {
		t.Fatal(err)
	}
	store := service.Secrets.(*memorySecrets)
	store.failSave = true
	if _, err := service.Apply(context.Background(), "", planPath, true, false); err == nil {
		t.Fatal("apply ignored a failed credential save")
	}
	if len(backend.connections) != 3 {
		t.Fatal("worker creation did not complete before the secret save failure")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	store.failSave = false
	if _, err := service.Apply(context.Background(), "", planPath, true, false); err != nil {
		t.Fatal(err)
	}
	if len(backend.connections) != 3 {
		t.Fatal("recovery duplicated the worker")
	}
	var keysAfter map[string]Fields
	if err := json.Unmarshal(readDocument(t, target)["keys"], &keysAfter); err != nil || keysAfter["worker"].text("id") != "conn_3" {
		t.Fatal("recovery did not write the worker ID under its object name")
	}
}

func TestNamedPlanFromDistinguishesOmittedAndEmptyObjects(t *testing.T) {
	for _, empty := range []bool{false, true} {
		service, backend, _, target, planPath := setup(t)
		if err := os.WriteFile(target, []byte(namedManifest), 0600); err != nil {
			t.Fatal(err)
		}
		planAndApply(t, service, target, planPath)
		source := filepath.Join(filepath.Dir(target), "dev.json")
		document := fields(map[string]any{"version": 1, "app": map[string]string{"id": "app_dev"}})
		if empty {
			document.set("keys", map[string]Fields{})
			document.set("routingKeys", map[string]Fields{})
		}
		writeDocument(t, source, document)
		plan, err := service.PlanFrom(context.Background(), "", target, planPath, source, true)
		if err != nil {
			t.Fatal(err)
		}
		if !empty {
			if plan.Operations != 0 {
				t.Fatal("omitted resource sections caused changes")
			}
			continue
		}
		if plan.Operations != 3 {
			t.Fatal("empty objects did not propose the managed resource removals")
		}
		before := len(backend.writes)
		if _, err := service.Apply(context.Background(), "", planPath, true, false); err == nil || problem.Safe(err).Code != "destructive_acceptance_required" {
			t.Fatal("removals bypassed explicit deletion approval")
		}
		if len(backend.writes) != before {
			t.Fatal("rejected removal plan made remote writes")
		}
	}
}
