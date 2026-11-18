package statestore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

type memoryBackend struct {
	mutex        sync.Mutex
	data         []byte
	version      int
	loseResponse bool
}

func (backend *memoryBackend) Read(context.Context) (Object, error) {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	if backend.version == 0 {
		return Object{}, nil
	}
	return Object{Data: bytes.Clone(backend.data), Version: fmt.Sprint(backend.version), Exists: true}, nil
}

func (backend *memoryBackend) Write(_ context.Context, data []byte, version string) (string, error) {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	if version == "" && backend.version != 0 || version != "" && version != fmt.Sprint(backend.version) {
		return "", Conflict()
	}
	backend.data = bytes.Clone(data)
	backend.version++
	if backend.loseResponse {
		backend.loseResponse = false
		return "", Unavailable()
	}
	return fmt.Sprint(backend.version), nil
}

func initializeBackend(t *testing.T) *memoryBackend {
	t.Helper()
	backend := &memoryBackend{}
	if _, err := Initialize(context.Background(), backend, json.RawMessage(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	return backend
}

func TestLockSerializesWritersAndStaleOwnerCannotOverwriteAfterUnlock(t *testing.T) {
	backend := initializeBackend(t)
	ctx := context.Background()
	first, err := Acquire(ctx, backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(ctx, backend); problem.Safe(err).Code != "state_locked" {
		t.Fatalf("second deployment was not blocked: %v", err)
	}
	if err := Unlock(ctx, backend, "different-lock"); problem.Safe(err).Code != "lock_id_mismatch" {
		t.Fatalf("wrong lock ID was accepted: %v", err)
	}
	if err := Unlock(ctx, backend, first.Document.Lock.ID); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(ctx, backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Save(ctx, json.RawMessage(`{"owner":"second"}`)); err != nil {
		t.Fatal(err)
	}
	if err := first.Save(ctx, json.RawMessage(`{"owner":"first"}`)); problem.Safe(err).Code != "remote_state_conflict" {
		t.Fatalf("stale owner overwrote newer state: %v", err)
	}
	if err := first.Close(); problem.Safe(err).Code != "state_lock_retained" {
		t.Fatalf("uncertain owner attempted to release a newer lock: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	document, _, err := Read(ctx, backend)
	if err != nil || document.Lock != nil || !bytes.Equal(document.Data, []byte(`{"owner":"second"}`)) {
		t.Fatalf("state or lock was lost: %v", err)
	}
}

func TestUnknownStorageWriteRetainsLockAndConfirmedProgress(t *testing.T) {
	backend := initializeBackend(t)
	session, err := Acquire(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	backend.loseResponse = true
	if err := session.Save(context.Background(), json.RawMessage(`{"created_id":"app_example"}`)); err == nil {
		t.Fatal("lost response was reported as success")
	}
	if err := session.Close(); problem.Safe(err).Code != "state_lock_retained" {
		t.Fatalf("uncertain write released the lock: %v", err)
	}
	document, _, err := Read(context.Background(), backend)
	if err != nil || document.Lock == nil || !bytes.Contains(document.Data, []byte("app_example")) {
		t.Fatalf("saved progress was lost: %v", err)
	}
}

func TestMissingStateAndInitializationNeverOverwriteAnExistingDeployment(t *testing.T) {
	backend := &memoryBackend{}
	if _, err := Acquire(context.Background(), backend); problem.Safe(err).Code != "remote_state_missing" {
		t.Fatalf("missing state was treated as empty: %v", err)
	}
	first, err := Initialize(context.Background(), backend, json.RawMessage(`{"app_id":"app_original"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(context.Background(), backend, json.RawMessage(`{"app_id":"app_other"}`)); err == nil {
		t.Fatal("initialization overwrote existing state")
	}
	stored, _, err := Read(context.Background(), backend)
	if err != nil || stored.ID != first.ID || !bytes.Contains(stored.Data, []byte("app_original")) {
		t.Fatalf("existing deployment was changed: %v", err)
	}
}
