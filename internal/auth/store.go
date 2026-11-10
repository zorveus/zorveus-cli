package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

var ErrNoLogin = errors.New("no saved login")

type Login struct {
	APIOrigin       string    `json:"api_origin"`
	DashboardOrigin string    `json:"dashboard_origin,omitempty"`
	ClientID        string    `json:"client_id"`
	Organization    string    `json:"organization"`
	AccessToken     string    `json:"access_token"`
	RefreshToken    string    `json:"refresh_token,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
	Scope           string    `json:"scope"`
	RefreshBlocked  bool      `json:"refresh_blocked,omitempty"`
}

type Store interface {
	Load() (Login, error)
	Save(Login) error
	Delete() error
}

type CredentialStore struct{}

const credentialService = "com.zorveus.cli"
const credentialAccount = "current-login"

func (CredentialStore) Load() (Login, error) {
	value, err := keyring.Get(credentialService, credentialAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return Login{}, ErrNoLogin
	}
	if err != nil {
		return Login{}, credentialStoreError()
	}
	var login Login
	if err := json.Unmarshal([]byte(value), &login); err != nil {
		return Login{}, problem.New("invalid_saved_login", "The saved login is unreadable. Remove the current-login entry for com.zorveus.cli from your OS credential store, then log in again.", 3)
	}
	return login, nil
}

func (CredentialStore) Save(login Login) error {
	value, err := json.Marshal(login)
	if err != nil {
		return credentialStoreError()
	}
	if err := keyring.Set(credentialService, credentialAccount, string(value)); err != nil {
		return credentialStoreError()
	}
	return nil
}

func (CredentialStore) Delete() error {
	err := keyring.Delete(credentialService, credentialAccount)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return credentialStoreError()
	}
	return nil
}

func credentialStoreError() error {
	return problem.New("credential_store_unavailable", "Could not access the OS credential store. Unlock your keychain or start your credential-store service, then retry. Credentials are never saved as plaintext.", 1)
}

func LockCredentials(ctx context.Context) (func(), error) {
	cacheDirectory, err := os.UserCacheDir()
	if err != nil {
		return nil, problem.New("lock_unavailable", "Could not find the user cache directory for the credential lock.", 1)
	}
	lockDirectory := filepath.Join(cacheDirectory, "zorveus")
	if err := os.MkdirAll(lockDirectory, 0700); err != nil {
		return nil, problem.New("lock_unavailable", "Could not create the credential lock directory.", 1)
	}
	return lockFile(ctx, filepath.Join(lockDirectory, "credentials.lock"))
}

func lockFile(ctx context.Context, path string) (func(), error) {
	lock := flock.New(path, flock.SetPermissions(0600))
	locked, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil || !locked {
		_ = lock.Close()
		return nil, problem.New("lock_unavailable", "Could not acquire the credential lock before the command was canceled.", 1)
	}
	return func() { _ = lock.Close() }, nil
}
