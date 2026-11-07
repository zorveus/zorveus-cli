package provision

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestPromotionRemovalsStillRequireExplicitApplyApproval(t *testing.T) {
	service, backend, _, target, planPath := setup(t)
	planAndApply(t, service, target, planPath)
	source := filepath.Join(filepath.Dir(target), "dev.json")
	writeDocument(t, source, fields(map[string]any{"version": 1, "app": map[string]string{"id": "app_dev"}, "keys": map[string]any{}, "routingKeys": map[string]any{}}))
	if _, err := Promote(context.Background(), source, target, true); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Plan(context.Background(), "", target, planPath)
	if err != nil || plan.Operations != 3 {
		t.Fatalf("expected two key revocations and a router revocation: %+v %v", plan.Changes, err)
	}
	before := len(backend.writes)
	if _, err := service.Apply(context.Background(), "", planPath, true, false); err == nil || problem.Safe(err).Code != "destructive_acceptance_required" {
		t.Fatal("promoted removals bypassed deletion approval")
	}
	if len(backend.writes) != before {
		t.Fatal("rejected apply changed production")
	}
	if _, err := service.Apply(context.Background(), "", planPath, true, true); err != nil {
		t.Fatal(err)
	}
}

func TestPromotedSharedSettingsKeepProductionCredentials(t *testing.T) {
	service, backend, _, target, planPath := setup(t)
	t.Setenv("PROMOTION_PROVIDER_KEY", "private-production-provider-key")
	document := readDocument(t, target)
	document.set("serviceKeys", map[string]any{"billing": map[string]any{"name": "Old billing", "scopes": []string{"product_users:read"}}})
	document.set("providerCredentials", map[string]any{"openai": map[string]any{"name": "Old OpenAI", "provider": "openai", "secretFromEnv": "PROMOTION_PROVIDER_KEY"}})
	writeDocument(t, target, document)
	planAndApply(t, service, target, planPath)
	source := filepath.Join(filepath.Dir(target), "dev.json")
	writeDocument(t, source, fields(map[string]any{
		"version": 1, "app": map[string]string{"id": "app_dev"},
		"serviceKeys":         map[string]any{"billing": map[string]any{"id": "dev_service", "name": "Updated billing", "scopes": []string{"product_users:read", "product_users:write"}}},
		"providerCredentials": map[string]any{"openai": map[string]any{"id": "dev_provider", "name": "Updated OpenAI", "routingMode": "manual_grant_only"}},
	}))
	if _, err := Promote(context.Background(), source, target, true); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Plan(context.Background(), "", target, planPath)
	if err != nil || plan.Operations != 2 {
		t.Fatalf("expected two shared metadata changes: %+v %v", plan.Changes, err)
	}
	for _, change := range plan.Changes {
		if change.Kind != "service_metadata" && change.Kind != "provider_metadata" {
			t.Fatalf("promotion would replace or rotate a target credential: %+v", change)
		}
	}
	if _, err := service.Apply(context.Background(), "", planPath, true, false); err != nil {
		t.Fatal(err)
	}
	desired, _, _, err := loadManifest(target)
	if err != nil {
		t.Fatal(err)
	}
	if desired.ServiceKeys[0].text("id") != "service_1" || desired.Providers[0].text("id") != "provider_1" || backend.providerVersions != 1 || backend.serviceRotations != 0 {
		t.Fatal("promotion changed the target's credential identity or secret")
	}
	if backend.services["service_1"].text("key_alias") != "Old billing" || backend.providers["provider_1"].text("credential_name") != "Old OpenAI" {
		t.Fatal("promotion changed shared credential display names")
	}
	if backend.services["service_1"].has("alias") || backend.providers["provider_1"].has("alias") {
		t.Fatal("local aliases reached the backend")
	}
	plan, err = service.Plan(context.Background(), "", target, planPath)
	if err != nil || plan.Operations != 0 {
		t.Fatalf("shared promotion did not converge: %+v %v", plan.Changes, err)
	}
}

func TestRejectedPromotionPreservesFilesAndPendingApplyState(t *testing.T) {
	for _, test := range []struct {
		name, source, code string
		pending            bool
	}{
		{"invalid JSON", `not JSON`, "invalid_json", false},
		{"incompatible model policies", `{"version":1,"app":{"id":"app_dev","allowedModels":["model_a"]}}`, "model_not_allowed", false},
		{"incomplete new router", `{
  "app": {
    "id": "app_dev"
  },
  "routingKeys": {
    "public": {
      "id": "dev_router"
    }
  },
  "version": 1
}`, "required_field", false},
		{"unfinished apply", `{"version":1,"app":{"id":"app_dev","name":"Updated"}}`, "apply_pending", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(dir, "dev.json"), filepath.Join(dir, "prod.json")
			original := []byte(`{
  "app": {
    "id": "app_prod",
    "name": "Production",
    "allowedModels": [
      "*"
    ]
  },
  "keys": {
    "backend": {
      "allowedModels": [
        "model_b"
      ],
      "id": "prod_key"
    }
  },
  "version": 1
}`)
			if err := os.WriteFile(source, []byte(test.source), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, original, 0600); err != nil {
				t.Fatal(err)
			}
			if test.pending {
				if err := os.Mkdir(directory(target), 0700); err != nil {
					t.Fatal(err)
				}
				journal := Journal{Version: 1, Config: target}
				if err := os.WriteFile(filepath.Join(directory(target), "pending.apply.json"), encoded(journal), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Promote(context.Background(), source, target, true)
			if err == nil || problem.Safe(err).Code != test.code {
				t.Fatalf("expected %s, got %v", test.code, err)
			}
			after, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatal("rejected promotion changed the target")
			}
		})
	}
}

func TestPromotionRejectsSameFileAndSymbolicLinks(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	link := filepath.Join(dir, "link.json")
	if err := os.WriteFile(config, []byte(`{"version":1,"app":{"id":"app_a"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Promote(context.Background(), config, config, true); err == nil || problem.Safe(err).Code != "promotion_same_file" {
		t.Fatal("promotion accepted the same file twice")
	}
	if err := os.Symlink(config, link); err != nil {
		t.Skip("symbolic links are unavailable:", err)
	}
	if _, err := Promote(context.Background(), config, link, true); err == nil || problem.Safe(err).Code != "promotion_files_required" {
		t.Fatal("promotion accepted a symbolic-link target")
	}
}
