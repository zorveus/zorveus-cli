package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/statestore"
)

type durableTestBackend struct {
	mutex   sync.Mutex
	data    []byte
	version int
	fail    func(statestore.Document) bool
	readErr error
}

func (backend *durableTestBackend) Read(context.Context) (statestore.Object, error) {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	if backend.readErr != nil {
		return statestore.Object{}, backend.readErr
	}
	if backend.version == 0 {
		return statestore.Object{}, nil
	}
	return statestore.Object{Data: bytes.Clone(backend.data), Version: fmt.Sprint(backend.version), Exists: true}, nil
}

func (backend *durableTestBackend) Write(_ context.Context, data []byte, version string) (string, error) {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	if version == "" && backend.version != 0 || version != "" && version != fmt.Sprint(backend.version) {
		return "", statestore.Conflict()
	}
	var document statestore.Document
	if err := json.Unmarshal(data, &document); err != nil {
		return "", err
	}
	if backend.fail != nil && backend.fail(document) {
		backend.fail = nil
		return "", statestore.Unavailable()
	}
	backend.data = bytes.Clone(data)
	backend.version++
	return fmt.Sprint(backend.version), nil
}

func durableSetup(t *testing.T) (*Service, *fakeBackend, *durableTestBackend, string, string) {
	t.Helper()
	service, api, _, config, plan := setup(t)
	backend := &durableTestBackend{}
	service.StateConfig = &statestore.Config{Backend: "s3", Bucket: "state-bucket", Key: "example/prod.json", Region: "us-east-1"}
	service.BackendFactory = func(context.Context, statestore.Config) (statestore.Backend, error) { return backend, nil }
	if _, err := service.InitializeState(context.Background(), config, false, false); err != nil {
		t.Fatal(err)
	}
	return service, api, backend, config, plan
}

func newRunner(t *testing.T, service *Service, data []byte) (*Service, string, string) {
	t.Helper()
	directory := t.TempDir()
	config, plan := filepath.Join(directory, "zorveus.json"), filepath.Join(directory, "zorveus.plan.json")
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	copy := *service
	copy.workspace = nil
	return &copy, config, plan
}

func TestDurableStateRestoresIDsAcrossFreshRunnersAndTracksRemovals(t *testing.T) {
	service, api, state, config, path := durableSetup(t)
	planAndApply(t, service, config, path)
	if bytes.Contains(state.data, []byte("private-")) {
		t.Fatal("remote state contains a management token or generated secret")
	}
	second, fresh, freshPlan := newRunner(t, service, []byte(newManifest))
	writes := len(api.writes)
	result, err := second.Plan(context.Background(), "", fresh, freshPlan)
	if err != nil || result.Operations != 0 || len(api.writes) != writes {
		t.Fatalf("fresh runner recreated existing resources: result=%+v error=%v", result, err)
	}
	unchanged, _ := os.ReadFile(fresh)
	if string(unchanged) != newManifest {
		t.Fatal("planning wrote restored IDs into the input manifest")
	}
	var desired map[string]any
	_ = json.Unmarshal([]byte(newManifest), &desired)
	delete(desired["keys"].(map[string]any), "secondary")
	desired["routingKeys"].(map[string]any)["public"].(map[string]any)["members"] = []string{"primary"}
	if err := os.WriteFile(fresh, encoded(desired), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = second.Plan(context.Background(), "", fresh, freshPlan)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range result.Changes {
		if change.Kind == "revoke_key" && change.ID == "conn_2" {
			found = true
		}
	}
	if !found {
		t.Fatal("removing a key forgot its previously managed ID")
	}
	if _, err := second.Apply(context.Background(), "", freshPlan, true, true); err != nil {
		t.Fatal(err)
	}
	if api.connections["conn_2"].text("status") != "revoked" {
		t.Fatal("removed key was not revoked")
	}
	info, err := second.InspectState(context.Background(), fresh)
	if err != nil || info.AppID != "app_a" || info.Lock != nil || !info.ApplyComplete {
		t.Fatalf("completed state was not persisted or unlocked: %+v %v", info, err)
	}
}

func TestDurablePlanCanBeAppliedFromAnotherCheckout(t *testing.T) {
	service, _, _, config, path := durableSetup(t)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	artifact, _ := os.ReadFile(path)
	second, fresh, freshPlan := newRunner(t, service, []byte(newManifest))
	if err := os.WriteFile(freshPlan, artifact, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := second.ApplyAt(context.Background(), "", freshPlan, fresh, true, false)
	if err != nil || result.AppID != "app_a" || result.Config != fresh {
		t.Fatalf("relocated apply failed: %+v %v", result, err)
	}
}

func TestDurableResumeAfterRunnerLossDoesNotRepeatCreations(t *testing.T) {
	service, api, _, config, path := durableSetup(t)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	failed := false
	service.Auth.API.HTTP.Transport = recoveryTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPatch && !failed {
			failed = true
			response := httptest.NewRecorder()
			response.WriteHeader(503)
			return response.Result(), nil
		}
		return api.RoundTrip(request)
	})
	if _, err := service.Apply(context.Background(), "", path, true, false); err == nil {
		t.Fatal("injected apply failure did not stop")
	}
	second, fresh, _ := newRunner(t, service, []byte(newManifest))
	if _, err := second.Plan(context.Background(), "", fresh, filepath.Join(filepath.Dir(fresh), "new.plan.json")); problem.Safe(err).Code != "apply_pending" {
		t.Fatalf("fresh runner ignored unfinished apply: %v", err)
	}
	if _, err := second.Resume(context.Background(), "", fresh, true, false); err != nil {
		t.Fatal(err)
	}
	apps, connections := 0, 0
	for _, write := range api.writes {
		if write == "POST /apps" {
			apps++
		}
		if write == "POST /app-connections/dashboard" {
			connections++
		}
	}
	if apps != 1 || connections != 2 {
		t.Fatalf("resume duplicated resources: apps=%d connections=%d writes=%v", apps, connections, api.writes)
	}
}

func TestStorageFailureBeforeCreationStopsAPIMutation(t *testing.T) {
	service, api, state, config, path := durableSetup(t)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	state.fail = func(document statestore.Document) bool {
		var snapshot durableSnapshot
		_ = json.Unmarshal(document.Data, &snapshot)
		return snapshot.Operation != nil && snapshot.Operation.Journal.Steps[0].Started
	}
	if _, err := service.Apply(context.Background(), "", path, true, false); err == nil || len(api.writes) != 0 {
		t.Fatalf("API mutation was sent after a failed checkpoint: %v writes=%v", err, api.writes)
	}
	info, err := service.InspectState(context.Background(), config)
	if err != nil || info.Lock == nil {
		t.Fatalf("uncertain storage did not retain its lock: %+v %v", info, err)
	}
	if err := service.UnlockState(context.Background(), config, info.Lock.ID, true); err != nil {
		t.Fatal(err)
	}
	second, fresh, _ := newRunner(t, service, []byte(newManifest))
	if _, err := second.Resume(context.Background(), "", fresh, true, false); err != nil {
		t.Fatal(err)
	}
}

func TestStorageFailureAfterCreationNeverDuplicatesAnUnknownResource(t *testing.T) {
	service, api, state, config, path := durableSetup(t)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	state.fail = func(document statestore.Document) bool {
		var snapshot durableSnapshot
		_ = json.Unmarshal(document.Data, &snapshot)
		return snapshot.Operation != nil && snapshot.Operation.Journal.Steps[0].ID != ""
	}
	if _, err := service.Apply(context.Background(), "", path, true, false); err == nil || len(api.writes) != 1 {
		t.Fatalf("creation failure was not recorded conservatively: %v writes=%v", err, api.writes)
	}
	info, err := service.InspectState(context.Background(), config)
	if err != nil || info.Lock == nil {
		t.Fatal("failed identity checkpoint lost its lock")
	}
	if err := service.UnlockState(context.Background(), config, info.Lock.ID, true); err != nil {
		t.Fatal(err)
	}
	second, fresh, _ := newRunner(t, service, []byte(newManifest))
	if _, err := second.Resume(context.Background(), "", fresh, true, false); err == nil || len(api.writes) != 1 {
		t.Fatalf("unknown creation was repeated: %v writes=%v", err, api.writes)
	}
}

func TestLocalStateCopyIsExplicitAndDoesNotOverwriteRemoteState(t *testing.T) {
	service, _, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	state := &durableTestBackend{}
	service.StateConfig = &statestore.Config{Backend: "s3", Bucket: "state", Key: "prod", Region: "us-east-1"}
	service.BackendFactory = func(context.Context, statestore.Config) (statestore.Backend, error) { return state, nil }
	if _, err := service.InitializeState(context.Background(), config, false, false); problem.Safe(err).Code != "local_state_exists" {
		t.Fatalf("local tracking was silently ignored: %v", err)
	}
	first, err := service.InitializeState(context.Background(), config, true, false)
	if err != nil || first.AppID != "app_a" {
		t.Fatalf("copy failed: %+v %v", first, err)
	}
	before := bytes.Clone(state.data)
	if result, err := service.InitializeState(context.Background(), config, true, false); err != nil || !result.AlreadyInitialized || !bytes.Equal(before, state.data) {
		t.Fatalf("repeated initialization did not reuse the existing deployment: %+v %v", result, err)
	}
}

func TestDurableStateNeverFallsBackToLocalTrackingAndRejectsStalePlans(t *testing.T) {
	service, api, _, config, path := durableSetup(t)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Plan(context.Background(), "", config, path+".stale"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatal(err)
	}
	writes := len(api.writes)
	if _, err := service.Apply(context.Background(), "", path+".stale", true, false); err == nil || len(api.writes) != writes {
		t.Fatalf("stale plan mutated resources: %v", err)
	}
	localOnly := *service
	localOnly.StateConfig = nil
	if _, err := localOnly.Plan(context.Background(), "", config, path+".local"); problem.Safe(err).Code != "state_backend_required" || len(api.writes) != writes {
		t.Fatalf("durable manifest fell back to local tracking: %v", err)
	}
}

func TestDurableProviderFingerprintSurvivesFreshRunnerWithoutLeakingInput(t *testing.T) {
	service, _, state, config, path := durableSetup(t)
	t.Setenv("TEST_DURABLE_PROVIDER_KEY", "private-provider-input")
	document := readDocument(t, config)
	document.set("providerCredentials", map[string]any{"primary": map[string]string{"name": "Provider", "provider": "openai", "secretFromEnv": "TEST_DURABLE_PROVIDER_KEY"}})
	writeDocument(t, config, document)
	original, _ := os.ReadFile(config)
	planAndApply(t, service, config, path)
	second, fresh, freshPlan := newRunner(t, service, original)
	second.Secrets = &memorySecrets{values: map[string]Credential{}}
	result, err := second.Plan(context.Background(), "", fresh, freshPlan)
	if err != nil || result.Operations != 0 {
		t.Fatalf("fresh runner lost provider fingerprint: %+v %v", result, err)
	}
	if bytes.Contains(state.data, []byte("private-provider-input")) {
		t.Fatal("provider input leaked into durable state")
	}
}

func TestDurableResumeRejectsChangedProviderInputOnAnotherRunner(t *testing.T) {
	service, api, _, config, path := durableSetup(t)
	t.Setenv("TEST_DURABLE_PROVIDER_KEY", "private-original-provider-input")
	document := readDocument(t, config)
	document.set("providerCredentials", map[string]any{"primary": map[string]string{"name": "Provider", "provider": "openai", "secretFromEnv": "TEST_DURABLE_PROVIDER_KEY"}})
	writeDocument(t, config, document)
	original, _ := os.ReadFile(config)
	service.Secrets.(*memorySecrets).failAccountPrefix = "provider/"
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), "", path, true, false); err == nil {
		t.Fatal("fingerprint storage failure was not injected")
	}
	writes := len(api.writes)
	second, fresh, _ := newRunner(t, service, original)
	second.Secrets = &memorySecrets{values: map[string]Credential{}}
	t.Setenv("TEST_DURABLE_PROVIDER_KEY", "private-changed-provider-input")
	if _, err := second.Resume(context.Background(), "", fresh, true, false); problem.Safe(err).Code != "secret_input_changed" || len(api.writes) != writes {
		t.Fatalf("changed provider input was accepted: %v writes=%v", err, api.writes)
	}
}

func TestDurableSnapshotWrittenOnWindowsCanBeReadOnAnotherOperatingSystem(t *testing.T) {
	service, _, state, config, path := durableSetup(t)
	planAndApply(t, service, config, path)
	var document statestore.Document
	_ = json.Unmarshal(state.data, &document)
	var snapshot durableSnapshot
	_ = json.Unmarshal(document.Data, &snapshot)
	snapshot.Operation.Plan.Config = `C:\checkout\project\zorveus.json`
	snapshot.Operation.Journal.Config = snapshot.Operation.Plan.Config
	document.Data = encoded(snapshot)
	if _, err := state.Write(context.Background(), encoded(document), fmt.Sprint(state.version)); err != nil {
		t.Fatal(err)
	}
	second, fresh, freshPlan := newRunner(t, service, []byte(newManifest))
	result, err := second.Plan(context.Background(), "", fresh, freshPlan)
	if err != nil || result.Operations != 0 {
		t.Fatalf("Windows path made shared state unusable: %+v %v", result, err)
	}
}
