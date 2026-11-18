package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/statestore"
)

type commandStateBackend struct {
	data    []byte
	version int
}

func (backend *commandStateBackend) Read(context.Context) (statestore.Object, error) {
	return statestore.Object{Data: bytes.Clone(backend.data), Version: fmt.Sprint(backend.version), Exists: backend.version != 0}, nil
}
func (backend *commandStateBackend) Write(_ context.Context, data []byte, version string) (string, error) {
	if version == "" && backend.version != 0 || version != "" && version != fmt.Sprint(backend.version) {
		return "", statestore.Conflict()
	}
	backend.data, backend.version = bytes.Clone(data), backend.version+1
	return fmt.Sprint(backend.version), nil
}

func TestStateCommandsInitializeInspectBackUpAndUnlockWithoutManagementLogin(t *testing.T) {
	directory := t.TempDir()
	config := filepath.Join(directory, "zorveus.json")
	if err := os.WriteFile(config, []byte(`{"version":1,"app":{"name":"Example","allowedModels":["*"]},"_state":{"backend":"s3","bucket":"state-bucket","key":"example/prod","region":"us-east-1"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	dependencies := testDependencies(&testStore{err: errors.New("management credentials must not be loaded")})
	backend := &commandStateBackend{}
	dependencies.StateBackendFactory = func(context.Context, statestore.Config) (statestore.Backend, error) { return backend, nil }
	tracking := filepath.Join(directory, ".zorveus")
	if err := os.Mkdir(tracking, 0700); err != nil {
		t.Fatal(err)
	}
	previous := []byte(`{"version":1,"app_id":"app_previous"}`)
	if err := os.WriteFile(filepath.Join(tracking, "zorveus.json.state.json"), previous, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(arguments ...string) (int, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		arguments = append(arguments, "--config", config, "--json", "--non-interactive")
		code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies)
		if stderr.Len() != 0 {
			t.Fatalf("JSON state command leaked diagnostics: %s", &stderr)
		}
		return code, stdout.String()
	}
	if code, result := run("state", "init", "--force", "--local"); code != 0 || !strings.Contains(result, `"backup_path":`) {
		t.Fatalf("init failed: %d %s", code, result)
	}
	backups, err := filepath.Glob(filepath.Join(tracking, "zorveus.json.state.backup-*", "zorveus.json.state.json"))
	if err != nil || len(backups) != 1 {
		t.Fatal("force did not back up local tracking")
	}
	if saved, err := os.ReadFile(backups[0]); err != nil || !bytes.Equal(saved, previous) {
		t.Fatal("force changed the previous tracking")
	}
	before := bytes.Clone(backend.data)
	if code, result := run("state", "init"); code != 0 || !strings.Contains(result, `"already_initialized":true`) || !bytes.Equal(before, backend.data) {
		t.Fatalf("repeated init was not idempotent: %d %s", code, result)
	}
	if code, result := run("state", "inspect"); code != 0 || !strings.Contains(result, `"revision":0`) {
		t.Fatalf("inspect failed: %d %s", code, result)
	}
	backup := filepath.Join(directory, "backup.json")
	if code, result := run("state", "pull", "--out", backup); code != 0 {
		t.Fatalf("backup failed: %d %s", code, result)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	session, err := statestore.Acquire(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := run("state", "unlock", "--lock-id", session.Document.Lock.ID); code != 2 {
		t.Fatal("unlock did not require approval")
	}
	if code, _ := run("state", "unlock", "--lock-id", "wrong-id", "--yes"); code != 4 {
		t.Fatal("unlock accepted the wrong lock ID")
	}
	if code, result := run("state", "unlock", "--lock-id", session.Document.Lock.ID, "--yes"); code != 0 {
		t.Fatalf("unlock failed: %d %s", code, result)
	}
	document, _, err := statestore.Read(context.Background(), backend)
	if err != nil || document.Lock != nil {
		t.Fatalf("state remained locked: %v", err)
	}
}

func TestStateConfigFileAcceptsBackendSettingsAndRejectsEmbeddedCredentials(t *testing.T) {
	directory := t.TempDir()
	config, settings := filepath.Join(directory, "zorveus.json"), filepath.Join(directory, "backend.json")
	if err := os.WriteFile(config, []byte(`{"version":1,"app":{"name":"Example","allowedModels":["*"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	dependencies := testDependencies(&testStore{})
	calls := 0
	dependencies.StateBackendFactory = func(_ context.Context, config statestore.Config) (statestore.Backend, error) {
		calls++
		if config.Key != "example/prod" {
			t.Fatal("backend configuration was not passed through")
		}
		return &commandStateBackend{}, nil
	}
	for _, includeCredential := range []bool{true, false} {
		value := map[string]string{"backend": "s3", "bucket": "state-bucket", "key": "example/prod", "region": "us-east-1"}
		if includeCredential {
			value["secretAccessKey"] = "private-test-secret"
		}
		data, _ := json.Marshal(value)
		if err := os.WriteFile(settings, data, 0600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"state", "init", "--config", config, "--state-config", settings, "--json"}, &stdout, &stderr, dependencies)
		if includeCredential && (code != 2 || calls != 0) || !includeCredential && (code != 0 || calls != 1) {
			t.Fatalf("unexpected config handling: code=%d calls=%d stdout=%s stderr=%s", code, calls, &stdout, &stderr)
		}
		if strings.Contains(stdout.String()+stderr.String(), "private-test-secret") {
			t.Fatal("configuration error exposed a credential")
		}
	}
}
