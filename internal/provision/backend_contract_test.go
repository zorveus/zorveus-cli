package provision

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func addSharedResources(t *testing.T, config string) {
	t.Helper()
	t.Setenv("TEST_PROVIDER_KEY", "private-provider-input")
	document := readDocument(t, config)
	document.set("serviceKeys", map[string]any{"backend": map[string]any{"name": "Backend", "scopes": []string{"product_users:read"}}})
	document.set("providerCredentials", map[string]any{"primary": map[string]string{"name": "Provider", "provider": "openai", "secretFromEnv": "TEST_PROVIDER_KEY"}})
	writeDocument(t, config, document)
}

func TestSharedMetadataUpdatesPreserveIDsAndSecrets(t *testing.T) {
	service, backend, _, config, path := setup(t)
	addSharedResources(t, config)
	planAndApply(t, service, config, path)
	version := backend.providers["provider_1"].text("active_secret_version_id")
	prefix := backend.services["service_1"].text("token_prefix")
	desired, _, _, err := loadManifest(config)
	if err != nil {
		t.Fatal(err)
	}
	desired.ServiceKeys[0].set("name", "Updated backend")
	desired.ServiceKeys[0].set("scopes", []string{"app_connections:write"})
	desired.Providers[0].set("name", "Updated provider")
	desired.Providers[0].set("routingMode", "manual_grant_only")
	desired.Providers[0].set("defaultModelPolicy", []string{"model_b", "model_a"})
	desired.Providers[0].set("providerConfig", map[string]string{"api_base": "https://provider.example.test"})
	document := readDocument(t, config)
	document.set("serviceKeys", desired.resourceValue("serviceKeys", desired.ServiceKeys))
	document.set("providerCredentials", desired.resourceValue("providerCredentials", desired.Providers))
	writeDocument(t, config, document)
	before := len(backend.writes)
	planAndApply(t, service, config, path+".metadata")
	expected := []string{"PATCH /orgs/org_a/service-keys/service_1", "PATCH /provider-credentials/provider_1"}
	if !slices.Equal(backend.writes[before:], expected) {
		t.Fatalf("metadata update wrote unexpected operations: %v", backend.writes[before:])
	}
	if backend.services["service_1"].text("token_prefix") != prefix || backend.providers["provider_1"].text("active_secret_version_id") != version {
		t.Fatal("metadata update replaced a secret")
	}
	if len(backend.services) != 1 || len(backend.providers) != 1 {
		t.Fatal("metadata update recreated a resource")
	}
	result, err := service.Plan(context.Background(), "", config, path+".unchanged")
	if err != nil || result.Operations != 0 {
		t.Fatalf("metadata update did not converge: %v %+v", err, result.Changes)
	}
	// Explicit null clears these fields, while omitted scopes and policy remain.
	desired, _, _, _ = loadManifest(config)
	desired.ServiceKeys[0].set("name", nil)
	desired.Providers[0].set("providerConfig", nil)
	document = readDocument(t, config)
	document.set("serviceKeys", desired.resourceValue("serviceKeys", desired.ServiceKeys))
	document.set("providerCredentials", desired.resourceValue("providerCredentials", desired.Providers))
	writeDocument(t, config, document)
	planAndApply(t, service, config, path+".clear")
	if string(backend.services["service_1"]["key_alias"]) != "null" || string(backend.providers["provider_1"]["provider_config"]) != "null" {
		t.Fatal("explicit clearing was not applied")
	}
	status, err := service.Status(context.Background(), "", config)
	if err != nil || !status.InSync {
		t.Fatalf("cleared settings report drift: %v %+v", err, status)
	}
}

func TestMetadataThenRotationCanResumeWithoutRepeatingEither(t *testing.T) {
	service, backend, _, config, path := setup(t)
	addSharedResources(t, config)
	planAndApply(t, service, config, path)
	desired, _, _, _ := loadManifest(config)
	desired.Providers[0].set("name", "Updated provider")
	document := readDocument(t, config)
	document.set("providerCredentials", desired.resourceValue("providerCredentials", desired.Providers))
	writeDocument(t, config, document)
	t.Setenv("TEST_PROVIDER_KEY", "private-replacement-input")
	if _, err := service.Plan(context.Background(), "", config, path+".update"); err != nil {
		t.Fatal(err)
	}
	store := service.Secrets.(*memorySecrets)
	store.failAccountPrefix = "provider/"
	_, err := service.Apply(context.Background(), "", path+".update", true, false)
	if err == nil || problem.Safe(err).ExitCode != 5 {
		t.Fatalf("expected interrupted secure delivery: %v", err)
	}
	before := len(backend.writes)
	store.failAccountPrefix = ""
	if _, err := service.Apply(context.Background(), "", path+".update", true, false); err != nil {
		t.Fatal(err)
	}
	if len(backend.writes) != before {
		t.Fatal("resuming repeated a metadata update or rotation")
	}
}

func TestUnknownServiceRotationDoesNotRotateAgain(t *testing.T) {
	service, backend, _, config, path := setup(t)
	addSharedResources(t, config)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	var plan Plan
	_, _, _ = loadJSON(path, &plan)
	store := service.Secrets.(*memorySecrets)
	store.failAccountPrefix = plan.ID + "/service:backend"
	_, err := service.Apply(context.Background(), "", path, true, false)
	if err == nil || problem.Safe(err).ExitCode != 5 {
		t.Fatalf("expected creation delivery failure: %v", err)
	}
	store.failAccountPrefix = ""
	backend.loseServiceRotationResponse = true
	_, err = service.Apply(context.Background(), "", path, true, false)
	if err == nil || problem.Safe(err).ExitCode != 5 {
		t.Fatalf("unknown rotation not recorded: %v", err)
	}
	before := len(backend.writes)
	_, err = service.Apply(context.Background(), "", path, true, false)
	if err == nil || problem.Safe(err).Code != "rotation_outcome_unknown" {
		t.Fatalf("retry accepted an unknown rotation: %v", err)
	}
	if len(backend.writes) != before || backend.serviceRotations != 1 {
		t.Fatal("unknown rotation caused an automatic second rotation")
	}
	_ = filepath.WalkDir(filepath.Dir(config), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "private-") {
			t.Errorf("rotation secret leaked to %s", path)
		}
		return nil
	})
}

func TestRejectedServiceRotationCanRetryAfterAccessIsRestored(t *testing.T) {
	service, backend, _, config, path := setup(t)
	addSharedResources(t, config)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	var plan Plan
	_, _, _ = loadJSON(path, &plan)
	store := service.Secrets.(*memorySecrets)
	store.failAccountPrefix = plan.ID + "/service:backend"
	_, _ = service.Apply(context.Background(), "", path, true, false)
	store.failAccountPrefix = ""
	backend.denyServiceRotation = true
	if _, err := service.Apply(context.Background(), "", path, true, false); err == nil {
		t.Fatal("server denial was ignored")
	}
	backend.denyServiceRotation = false
	if _, err := service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatal(err)
	}
	if backend.serviceRotations != 1 {
		t.Fatal("rejected rotation was incorrectly treated as an unknown successful mutation")
	}
}

func TestExpiredServiceKeyAndInvalidProviderConfigStopBeforeWrites(t *testing.T) {
	for _, failure := range []string{"expired", "revoked", "provider_config"} {
		t.Run(failure, func(t *testing.T) {
			service, backend, _, config, path := setup(t)
			addSharedResources(t, config)
			planAndApply(t, service, config, path)
			desired, _, _, _ := loadManifest(config)
			desired.ServiceKeys[0].set("name", "Updated")
			switch failure {
			case "expired":
				backend.services["service_1"].set("expires_at", time.Now().Add(-time.Minute))
			case "revoked":
				backend.services["service_1"].set("status", "revoked")
			case "provider_config":
				backend.providers["provider_1"].set("provider", "azure")
				delete(desired.Providers[0], "provider")
				desired.Providers[0].set("providerConfig", nil)
			}
			document := readDocument(t, config)
			document.set("serviceKeys", desired.resourceValue("serviceKeys", desired.ServiceKeys))
			document.set("providerCredentials", desired.resourceValue("providerCredentials", desired.Providers))
			writeDocument(t, config, document)
			before := len(backend.writes)
			_, err := service.Plan(context.Background(), "", config, path+".invalid")
			if err == nil {
				t.Fatal("invalid shared resource update accepted")
			}
			if len(backend.writes) != before {
				t.Fatal("invalid shared resource update mutated the backend")
			}
		})
	}
}

func TestServiceNameNullIsOnlyAllowedForExistingKeys(t *testing.T) {
	for _, id := range []string{"", "service_1"} {
		service, _, _, config, path := setup(t)
		entry := map[string]any{"name": nil, "scopes": []string{"product_users:read"}}
		if id != "" {
			entry["id"] = id
		}
		document := readDocument(t, config)
		document.set("serviceKeys", map[string]any{"backend": entry})
		writeDocument(t, config, document)
		_, err := service.Plan(context.Background(), "", config, path)
		if id == "" && (err == nil || problem.Safe(err).ExitCode != 2) {
			t.Fatal("a new service key accepted a null name")
		}
		if id != "" && (err == nil || problem.Safe(err).Code != "resource_not_found") {
			t.Fatalf("existing null alias was rejected by local validation: %v", err)
		}
	}
}
