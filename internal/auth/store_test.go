package auth

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCredentialLockCoordinatesSeparateProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.lock")
	unlock, err := lockFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	worker := func(expectLocked bool) {
		t.Helper()
		command := exec.Command(os.Args[0], "-test.run=^TestCredentialLockWorker$")
		command.Env = append(os.Environ(), "ZORVEUS_TEST_LOCK_PATH="+path)
		if expectLocked {
			command.Env = append(command.Env, "ZORVEUS_TEST_EXPECT_LOCKED=1")
		}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("lock worker failed: %v\n%s", err, output)
		}
	}
	worker(true)
	unlock()
	worker(false)
}

func TestCredentialLockWorker(t *testing.T) {
	path := os.Getenv("ZORVEUS_TEST_LOCK_PATH")
	if path == "" {
		t.Skip("only runs in the lock subprocess")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	unlock, err := lockFile(ctx, path)
	if os.Getenv("ZORVEUS_TEST_EXPECT_LOCKED") == "1" {
		if err == nil {
			unlock()
			t.Fatal("another process acquired the held credential lock")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}
