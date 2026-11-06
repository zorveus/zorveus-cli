package provision

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestImportPreservesSelectorsAndImmediatelyConverges(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	source, _ := os.ReadFile(config)
	imported := filepath.Join(filepath.Dir(config), "imported.json")
	before := len(backend.writes)
	result, err := service.Import(context.Background(), "", "app_a", imported)
	if err != nil {
		t.Fatal(err)
	}
	if result.Keys != 2 || result.Routers != 1 {
		t.Fatalf("incomplete import: %+v", result)
	}
	if len(backend.writes) != before {
		t.Fatal("import wrote to the backend")
	}
	after, _ := os.ReadFile(config)
	if string(source) != string(after) {
		t.Fatal("import modified another manifest")
	}
	desired, _, _, err := loadManifest(imported)
	if err != nil {
		t.Fatal(err)
	}
	if desired.Keys[0].text("routingAlias") != "primary" || desired.Keys[1].text("routingAlias") != "backup" || desired.Routers[0].text("id") != "router_1" {
		t.Fatal("import changed identities or selectors")
	}
	plan, err := service.Plan(context.Background(), "", imported, path+".imported")
	if err != nil || plan.Operations != 0 {
		t.Fatalf("import would mutate existing resources: %v %+v", err, plan.Changes)
	}
	status, err := service.Status(context.Background(), "", imported)
	if err != nil || !status.InSync || status.LastApply != nil {
		t.Fatalf("unexpected imported status: %v %+v", err, status)
	}
	if _, err := service.Import(context.Background(), "", "app_a", imported); err == nil || problem.Safe(err).Code != "destination_exists" {
		t.Fatalf("existing import destination not refused: %v", err)
	}
}

func TestImportRejectsConflictingSelectorsWithoutChangingRoutes(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	router := fields(backend.routers["router_1"])
	router.set("api_key_id", "router_2")
	var members []Fields
	_ = json.Unmarshal(router["members"], &members)
	members[0].set("selector_alias", "different")
	router.set("members", members)
	backend.routers["router_2"] = router
	imported := filepath.Join(filepath.Dir(config), "conflicting.json")
	before := len(backend.writes)
	_, err := service.Import(context.Background(), "", "app_a", imported)
	if err == nil || problem.Safe(err).Code != "import_alias_conflict" {
		t.Fatalf("ambiguous aliases imported: %v", err)
	}
	if len(backend.writes) != before {
		t.Fatal("import rewrote selectors")
	}
	if _, err := os.Stat(imported); !os.IsNotExist(err) {
		t.Fatal("failed import wrote a manifest")
	}
	if _, err := os.Stat(statePath(imported)); !os.IsNotExist(err) {
		t.Fatal("failed import adopted managed resources")
	}
}

func TestImportGeneratesAliasesOnlyForUnroutedKeys(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	backend.routers = map[string]Fields{}
	imported := filepath.Join(filepath.Dir(config), "unrouted.json")
	if _, err := service.Import(context.Background(), "", "app_a", imported); err != nil {
		t.Fatal(err)
	}
	desired, _, _, err := loadManifest(imported)
	if err != nil {
		t.Fatal(err)
	}
	if desired.Keys[0].text("routingAlias") != "key_1" || desired.Keys[1].text("routingAlias") != "key_2" {
		t.Fatal("unrouted aliases are not deterministic")
	}
}

func TestStatusShowsDriftRevocationMissingResourcesAndLastApply(t *testing.T) {
	service, backend, store, config, path := setup(t)
	planAndApply(t, service, config, path)
	before := len(backend.writes)
	stateBefore, _ := os.ReadFile(statePath(config))
	manifestBefore, _ := os.ReadFile(config)
	store.login.Scope = "apps:read app_connections:read routing_keys:read"
	status, err := service.Status(context.Background(), "", config)
	if err != nil || !status.InSync || status.LastApply == nil || !status.LastApply.Complete {
		t.Fatalf("unexpected status: %v %+v", err, status)
	}
	backend.app.set("app_name", "Remote change")
	backend.caps["conn_1"].set("amount", "200.0")
	backend.connections["conn_2"].set("status", "revoked")
	delete(backend.routers, "router_1")
	status, err = service.Status(context.Background(), "", config)
	if err != nil || status.InSync {
		t.Fatalf("drift not reported: %v %+v", err, status)
	}
	if len(status.Resources[0].Changes) != 1 || status.Resources[1].Changes[0].Field != "cap" || status.Resources[2].Status != "revoked" || status.Resources[3].Status != "not_found" {
		t.Fatalf("incomplete drift report: %+v", status.Resources)
	}
	if len(backend.writes) != before {
		t.Fatal("status mutated the backend")
	}
	stateAfter, _ := os.ReadFile(statePath(config))
	manifestAfter, _ := os.ReadFile(config)
	if string(stateBefore) != string(stateAfter) || string(manifestBefore) != string(manifestAfter) {
		t.Fatal("status modified local configuration or ownership")
	}
}

func TestStatusReportsPendingRemovalsAndPreservesOmittedSections(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	writeDocument(t, config, fields(map[string]any{"version": 1, "app": map[string]string{"id": "app_a"}, "keys": []any{}, "routingKeys": []any{}}))
	status, err := service.Status(context.Background(), "", config)
	if err != nil || status.InSync || len(status.Resources) != 4 {
		t.Fatalf("managed removals not reported: %v %+v", err, status.Resources)
	}
	for _, resource := range status.Resources[1:] {
		if resource.Matches || len(resource.Changes) != 1 || string(resource.Changes[0].Desired) != `"revoked"` {
			t.Fatalf("incorrect removal status: %+v", resource)
		}
	}
	writeDocument(t, config, fields(map[string]any{"version": 1, "app": map[string]string{"id": "app_a"}}))
	status, err = service.Status(context.Background(), "", config)
	if err != nil || !status.InSync || len(status.Resources) != 1 {
		t.Fatalf("omitted resources reported as removals: %v %+v", err, status.Resources)
	}
	if backend.connections["conn_1"].text("status") != "active" {
		t.Fatal("status revoked a resource")
	}
}

func TestStatusDoesNotCreateLocalStateOrReadSecretInputs(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	imported := filepath.Join(filepath.Dir(config), "no-state", "zorveus.json")
	if err := os.Mkdir(filepath.Dir(imported), 0700); err != nil {
		t.Fatal(err)
	}
	writeDocument(t, imported, fields(map[string]any{"version": 1, "app": map[string]string{"id": "app_a"}}))
	if _, err := service.Status(context.Background(), "", imported); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory(imported)); !os.IsNotExist(err) {
		t.Fatal("read-only status created a state directory")
	}
	if len(backend.reads) == 0 {
		t.Fatal("status did not read remote resources")
	}
}

func TestStatusReportsIncompleteApply(t *testing.T) {
	service, backend, _, config, path := setup(t)
	backend.loseCreateResponse = true
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	_, _ = service.Apply(context.Background(), "", path, true, false)
	before := len(backend.writes)
	status, err := service.Status(context.Background(), "", config)
	if err != nil || status.InSync || status.LastApply == nil || status.LastApply.Complete || status.LastApply.Completed != 0 {
		t.Fatalf("unfinished apply not reported: %v %+v", err, status)
	}
	if strings.Contains(string(encoded(status)), "private-") || len(backend.writes) != before {
		t.Fatal("pending status leaked credentials or repeated creation")
	}
}
