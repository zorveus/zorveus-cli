package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestLoginIndexContainsReferencesAndNoTokens(t *testing.T) {
	keyring.MockInit()
	store := CredentialStore{Directory: t.TempDir()}
	login := activeLogin("https://api.example.test", "org_a")
	if err := store.Write(SavedLogins{Version: 1, Logins: []Login{login}, Default: reference(login)}); err != nil {
		t.Fatal(err)
	}
	path, _ := store.indexPath()
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), login.AccessToken) || strings.Contains(string(data), login.RefreshToken) {
		t.Fatalf("index contains a token or could not be read: %v", err)
	}
	var index loginIndex
	if json.Unmarshal(data, &index) != nil || len(index.Logins) != 1 || index.Default != reference(login) {
		t.Fatal("index lost its organization or default")
	}
	value, err := keyring.Get(credentialService, loginAccount(reference(login)))
	if err != nil || !strings.Contains(value, login.AccessToken) || !strings.Contains(value, login.RefreshToken) {
		t.Fatalf("tokens were not saved in their own keyring entry: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(store.Directory, ".logins-*"))
	if len(files) != 0 {
		t.Fatal("temporary login index was left behind")
	}
}

func TestMissingOrganizationCredentialDoesNotBlockOtherLoginsOrRepair(t *testing.T) {
	a := activeLogin("https://api.example.test", "org_a")
	b := activeLogin(a.APIOrigin, "org_b")
	service := accountService(t, a, b)
	if err := keyring.Delete(credentialService, loginAccount(reference(a))); err != nil {
		t.Fatal(err)
	}
	if _, err := service.loadLogin(context.Background(), LoginTarget{Organization: b.Organization}); err != nil {
		t.Fatalf("missing entry blocked a healthy organization's login: %v", err)
	}
	if _, err := service.loadLogin(context.Background(), LoginTarget{Organization: a.Organization}); problem.Safe(err).Code != "login_required" {
		t.Fatalf("missing entry did not require login: %v", err)
	}
	if _, err := service.replaceLogin(context.Background(), a); err != nil {
		t.Fatalf("could not repair the missing entry: %v", err)
	}
	if login, err := service.loadLogin(context.Background(), LoginTarget{Organization: a.Organization}); err != nil || login.AccessToken != a.AccessToken {
		t.Fatalf("repaired login was not usable: %v", err)
	}
}

func TestUnwritableLoginIndexDoesNotCreateCredentialEntry(t *testing.T) {
	keyring.MockInit()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	store := CredentialStore{Directory: path}
	login := activeLogin("https://api.example.test", "org_a")
	if err := store.Write(SavedLogins{Version: 1, Logins: []Login{login}}); err == nil {
		t.Fatal("invalid index path was accepted")
	}
	if _, err := keyring.Get(credentialService, loginAccount(reference(login))); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("failed index preparation wrote credentials")
	}
}

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
