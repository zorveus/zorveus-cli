package provision

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func writeDocument(t *testing.T, config string, document Fields) {
	t.Helper()
	if err := os.WriteFile(config, encoded(document), 0600); err != nil {
		t.Fatal(err)
	}
}

func readDocument(t *testing.T, config string) Fields {
	t.Helper()
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var document Fields
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func TestPublicAppConsentPricingAndRedirectUpdates(t *testing.T) {
	service, backend, store, config, path := setup(t)
	store.login.Scope = strings.Replace(fullScope, "apps:rotate_secret", "", 1)
	document := fields(map[string]any{"version": 1, "app": map[string]any{
		"name": "Public demo", "allowedModels": []string{"*"}, "creditUnitsPerUsd": "1000.000", "productUserChargeMultiplier": "1.5",
		"oauth":   map[string]any{"clientType": "public", "redirectUris": []string{"https://example.test/old"}},
		"consent": map[string]any{"defaultCap": map[string]string{"amount": "10.00", "currency": "USD", "period": "monthly"}, "minimumCapAmount": "5", "maximumCapAmount": "20", "allowUnlimitedCap": false},
	}})
	writeDocument(t, config, document)
	planAndApply(t, service, config, path)
	if len(service.Secrets.(*memorySecrets).values) != 0 {
		t.Fatal("public app unexpectedly delivered a client secret")
	}
	if backend.app.text("product_user_charge_multiplier") != "1.5" || backend.app.text("default_cap_amount") != "10.00" {
		t.Fatal("app settings were not sent")
	}
	document = readDocument(t, config)
	var app Fields
	_ = json.Unmarshal(document["app"], &app)
	app.set("oauth", map[string]any{"clientType": "public", "redirectUris": []string{"https://example.test/new"}})
	app.set("consent", map[string]any{"defaultCap": nil, "maximumCapAmount": "30"})
	document.set("app", app)
	writeDocument(t, config, document)
	if _, err := service.Plan(context.Background(), "", config, path+".update"); err != nil {
		t.Fatal(err)
	}
	before := len(backend.writes)
	if _, err := service.Apply(context.Background(), "", path+".update", true, false); err == nil {
		t.Fatal("redirect removal accepted without --allow-delete")
	}
	if len(backend.writes) != before {
		t.Fatal("mutation preceded destructive approval")
	}
	if _, err := service.Apply(context.Background(), "", path+".update", true, true); err != nil {
		t.Fatal(err)
	}
	if backend.app.text("minimum_cap_amount") != "5" || backend.app.text("product_user_charge_multiplier") != "1.5" {
		t.Fatal("omitted app settings changed")
	}
	result, err := service.Plan(context.Background(), "", config, path+".next")
	if err != nil || result.Operations != 0 {
		t.Fatalf("app settings did not converge: %v %+v", err, result.Changes)
	}
	status, err := service.Status(context.Background(), "", config)
	if err != nil || !status.InSync {
		t.Fatalf("unexpected app drift: %v %+v", err, status.Resources)
	}
}

func TestPartialConsentUsesExistingBoundsBeforeWriting(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	backend.app.set("default_cap_amount", "10")
	backend.app.set("minimum_cap_amount", "5")
	backend.app.set("maximum_cap_amount", "20")
	writeDocument(t, config, fields(map[string]any{"version": 1, "app": map[string]any{"id": "app_a", "consent": map[string]string{"maximumCapAmount": "8"}}}))
	before := len(backend.writes)
	_, err := service.Plan(context.Background(), "", config, path+".invalid")
	if err == nil || problem.Safe(err).Code != "invalid_cap_policy" {
		t.Fatalf("remote consent bounds not validated: %v", err)
	}
	if len(backend.writes) != before {
		t.Fatal("invalid consent mutated the backend")
	}
}

func TestSharedCredentialsUseSafePlansAndExplicitSecretInputs(t *testing.T) {
	service, backend, _, config, path := setup(t)
	t.Setenv("TEST_PROVIDER_KEY", "private-provider-input")
	document := readDocument(t, config)
	document.set("serviceKeys", []any{map[string]any{"name": "Backend", "scopes": []string{"product_users:read"}}})
	document.set("providerCredentials", []any{map[string]any{"name": "Primary OpenAI", "provider": "openai", "secretFromEnv": "TEST_PROVIDER_KEY", "routingMode": "manual_grant_only", "defaultModelPolicy": []string{"model_a"}}})
	writeDocument(t, config, document)
	planAndApply(t, service, config, path)
	if backend.providerSecret != "private-provider-input" {
		t.Fatal("provider secret input was not delivered")
	}
	serviceSaved := false
	for _, credential := range service.Secrets.(*memorySecrets).values {
		if credential.ResourceID == "service_1" && credential.Secret == "private-service-secret" {
			serviceSaved = true
		}
	}
	if !serviceSaved {
		t.Fatal("service key was not saved securely")
	}
	desired, _, _, err := loadManifest(config)
	if err != nil {
		t.Fatal(err)
	}
	if desired.ServiceKeys[0].text("id") != "service_1" || desired.Providers[0].text("id") != "provider_1" {
		t.Fatal("shared resource IDs missing")
	}
	before := len(backend.writes)
	status, err := service.Status(context.Background(), "", config)
	if err != nil || !status.InSync {
		t.Fatalf("unexpected shared resource drift: %v %+v", err, status.Resources)
	}
	if len(backend.writes) != before {
		t.Fatal("status mutated shared resources")
	}
	unchanged, err := service.Plan(context.Background(), "", config, path+".unchanged")
	if err != nil || unchanged.Operations != 0 {
		t.Fatalf("unchanged shared credentials did not converge: %v %+v", err, unchanged.Changes)
	}
	t.Setenv("TEST_PROVIDER_KEY", "private-provider-replacement")
	result, err := service.Plan(context.Background(), "", config, path+".rotate")
	if err != nil || result.Operations != 1 || result.Changes[0].Kind != "provider_secret" {
		t.Fatalf("expected explicit provider rotation: %v %+v", err, result.Changes)
	}
	if _, err := service.Apply(context.Background(), "", path+".rotate", true, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(backend.writes[before:], []string{"POST /provider-credentials/provider_1/rotate"}) {
		t.Fatalf("unexpected shared resource writes: %v", backend.writes[before:])
	}
	if backend.providerSecret != "private-provider-replacement" {
		t.Fatal("replacement provider input missing")
	}
	unchanged, err = service.Plan(context.Background(), "", config, path+".rotated")
	if err != nil || unchanged.Operations != 0 {
		t.Fatalf("rotated input did not converge: %v %+v", err, unchanged.Changes)
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
			t.Errorf("credential leaked to %s", path)
		}
		return nil
	})
}

func TestMissingProviderInputStopsBeforeAnyRemoteWrite(t *testing.T) {
	service, backend, _, config, path := setup(t)
	document := readDocument(t, config)
	document.set("providerCredentials", []any{map[string]string{"name": "Provider", "provider": "openai", "secretFromEnv": "TEST_MISSING_PROVIDER"}})
	writeDocument(t, config, document)
	t.Setenv("TEST_MISSING_PROVIDER", "")
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	_, err := service.Apply(context.Background(), "", path, true, false)
	if err == nil || problem.Safe(err).Code != "secret_input_required" {
		t.Fatalf("missing input was accepted: %v", err)
	}
	if len(backend.writes) != 0 {
		t.Fatal("writes preceded secret input validation")
	}
}

func TestSharedResourceOmissionDoesNotRevokeOrganizationResources(t *testing.T) {
	service, backend, _, config, path := setup(t)
	document := readDocument(t, config)
	document.set("serviceKeys", []any{map[string]any{"name": "Backend", "scopes": []string{"product_users:read"}}})
	writeDocument(t, config, document)
	planAndApply(t, service, config, path)
	document = readDocument(t, config)
	document.set("serviceKeys", []any{})
	writeDocument(t, config, document)
	before := len(backend.writes)
	planAndApply(t, service, config, path+".removed")
	if len(backend.writes) != before || backend.services["service_1"].text("status") != "active" {
		t.Fatal("removing a manifest reference revoked a shared key")
	}
	if _, err := service.RevokeServiceKey(context.Background(), "", "service_1", false); err == nil {
		t.Fatal("organization revocation accepted without confirmation")
	}
	if _, err := service.RevokeServiceKey(context.Background(), "", "service_1", true); err != nil {
		t.Fatal(err)
	}
	if backend.services["service_1"].text("status") != "revoked" {
		t.Fatal("explicit service-key revocation failed")
	}
	before = len(backend.writes)
	if _, err := service.RevokeServiceKey(context.Background(), "", "service_1", true); err != nil {
		t.Fatal(err)
	}
	if len(backend.writes) != before {
		t.Fatal("revoked service key was revoked again")
	}
}

func TestProviderResumePreservesOriginalInputAndDoesNotRepeatCreation(t *testing.T) {
	service, backend, _, config, path := setup(t)
	t.Setenv("TEST_PROVIDER_KEY", "private-original-input")
	document := readDocument(t, config)
	document.set("providerCredentials", []any{map[string]string{"name": "Provider", "provider": "openai", "secretFromEnv": "TEST_PROVIDER_KEY"}})
	writeDocument(t, config, document)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	store := service.Secrets.(*memorySecrets)
	store.failAccountPrefix = "provider/"
	_, err := service.Apply(context.Background(), "", path, true, false)
	if err == nil || problem.Safe(err).ExitCode != 5 {
		t.Fatalf("expected interrupted fingerprint delivery: %v", err)
	}
	before := len(backend.writes)
	store.failAccountPrefix = ""
	t.Setenv("TEST_PROVIDER_KEY", "private-changed-input")
	_, err = service.Apply(context.Background(), "", path, true, false)
	if err == nil || problem.Safe(err).Code != "secret_input_changed" {
		t.Fatalf("changed retry input accepted: %v", err)
	}
	if len(backend.writes) != before {
		t.Fatal("changed input mutated existing resources")
	}
	t.Setenv("TEST_PROVIDER_KEY", "private-original-input")
	if _, err := service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatal(err)
	}
	for _, write := range backend.writes[before:] {
		if write == "POST /apps" || write == "POST /provider-credentials" {
			t.Fatal("provider or app creation repeated after a local save failure")
		}
	}
}

func TestProviderRotationResumesAfterFingerprintSaveFailure(t *testing.T) {
	service, backend, _, config, path := setup(t)
	t.Setenv("TEST_PROVIDER_KEY", "private-original-input")
	document := readDocument(t, config)
	document.set("providerCredentials", []any{map[string]string{"name": "Provider", "provider": "openai", "secretFromEnv": "TEST_PROVIDER_KEY"}})
	writeDocument(t, config, document)
	planAndApply(t, service, config, path)
	t.Setenv("TEST_PROVIDER_KEY", "private-replacement-input")
	if _, err := service.Plan(context.Background(), "", config, path+".rotate"); err != nil {
		t.Fatal(err)
	}
	store := service.Secrets.(*memorySecrets)
	store.failAccountPrefix = "provider/"
	_, err := service.Apply(context.Background(), "", path+".rotate", true, false)
	if err == nil || problem.Safe(err).ExitCode != 5 {
		t.Fatalf("expected pending rotation delivery: %v", err)
	}
	before := len(backend.writes)
	store.failAccountPrefix = ""
	if _, err := service.Apply(context.Background(), "", path+".rotate", true, false); err != nil {
		t.Fatal(err)
	}
	if len(backend.writes) != before {
		t.Fatal("successful rotation repeated after a local save failure")
	}
}

func TestServiceSecretRecoveryUsesStableIDRotation(t *testing.T) {
	service, backend, _, config, path := setup(t)
	document := readDocument(t, config)
	document.set("serviceKeys", []any{map[string]any{"name": "Backend", "scopes": []string{"product_users:read"}}})
	writeDocument(t, config, document)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	var plan Plan
	_, _, _ = loadJSON(path, &plan)
	store := service.Secrets.(*memorySecrets)
	store.failAccountPrefix = plan.ID + "/service:0"
	_, err := service.Apply(context.Background(), "", path, true, false)
	if err == nil || problem.Safe(err).ExitCode != 5 {
		t.Fatalf("service secret delivery failure not recorded: %v", err)
	}
	before := len(backend.writes)
	store.failAccountPrefix = ""
	if _, err = service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatal(err)
	}
	if backend.serviceRotations != 1 || len(backend.services) != 1 {
		t.Fatal("service key recovery did not rotate exactly one existing key")
	}
	if store.values[plan.ID+"/service:0"].Secret != "private-rotated-service-secret" {
		t.Fatal("replacement service secret was not delivered")
	}
	for _, write := range backend.writes[before:] {
		if write == "POST /orgs/org_a/service-keys" {
			t.Fatal("service key was duplicated during recovery")
		}
	}
}
