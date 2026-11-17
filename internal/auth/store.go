package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

var ErrNoLogin = errors.New("no saved login")

type Login struct {
	APIOrigin        string    `json:"api_origin"`
	DashboardOrigin  string    `json:"dashboard_origin,omitempty"`
	ClientID         string    `json:"client_id"`
	Organization     string    `json:"organization"`
	OrganizationName string    `json:"organization_name,omitempty"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	ExpiresAt        time.Time `json:"expires_at"`
	Scope            string    `json:"scope"`
	RefreshBlocked   bool      `json:"refresh_blocked,omitempty"`
}

type Store interface {
	Read() (SavedLogins, error)
	Write(SavedLogins) error
}

type LoginReference struct {
	APIOrigin    string `json:"api_origin"`
	Organization string `json:"organization"`
}

type SavedLogins struct {
	Version int            `json:"version"`
	Logins  []Login        `json:"logins"`
	Default LoginReference `json:"default"`
}

type CredentialStore struct {
	Directory string
}

type loginIndex struct {
	Version int            `json:"version"`
	Logins  []indexedLogin `json:"logins"`
	Default LoginReference `json:"default"`
}

type indexedLogin struct {
	APIOrigin    string `json:"api_origin"`
	Organization string `json:"organization"`
	Name         string `json:"name,omitempty"`
}

const credentialService = "com.zorveus.cli"
const credentialAccount = "current-login"

func (store CredentialStore) Read() (SavedLogins, error) {
	path, err := store.indexPath()
	if err != nil {
		return SavedLogins{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return readLegacyLogin()
	}
	if err != nil {
		return SavedLogins{}, credentialStoreError()
	}
	var index loginIndex
	if json.Unmarshal(data, &index) != nil || index.Version != 1 {
		return SavedLogins{}, invalidSavedLogins()
	}
	saved := SavedLogins{Version: 1, Logins: []Login{}, Default: index.Default}
	for _, entry := range index.Logins {
		target := LoginReference{APIOrigin: entry.APIOrigin, Organization: entry.Organization}
		if _, duplicate := saved.find(target); duplicate {
			return SavedLogins{}, invalidSavedLogins()
		}
		value, err := keyring.Get(credentialService, loginAccount(target))
		if errors.Is(err, keyring.ErrNotFound) {
			// Keep the organization selectable so login can replace a missing entry.
			saved.Logins = append(saved.Logins, Login{APIOrigin: target.APIOrigin, Organization: target.Organization, OrganizationName: entry.Name})
			continue
		}
		if err != nil {
			return SavedLogins{}, credentialStoreError()
		}
		var login Login
		if json.Unmarshal([]byte(value), &login) != nil || reference(login) != target {
			return SavedLogins{}, invalidSavedLogins()
		}
		saved.Logins = append(saved.Logins, login)
	}
	return saved, nil
}

func readLegacyLogin() (SavedLogins, error) {
	value, err := keyring.Get(credentialService, credentialAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return SavedLogins{}, ErrNoLogin
	}
	if err != nil {
		return SavedLogins{}, credentialStoreError()
	}
	// Read the previous single-login format without losing that session.
	var previous Login
	if json.Unmarshal([]byte(value), &previous) != nil || previous.Organization == "" {
		return SavedLogins{}, invalidSavedLogins()
	}
	return SavedLogins{Version: 1, Logins: []Login{previous}, Default: reference(previous)}, nil
}

func (store CredentialStore) Write(saved SavedLogins) error {
	path, err := store.indexPath()
	if err != nil {
		return err
	}
	previous, err := store.Read()
	if err != nil && !errors.Is(err, ErrNoLogin) {
		return err
	}
	index := loginIndex{Version: 1, Logins: []indexedLogin{}, Default: saved.Default}
	for _, login := range saved.Logins {
		index.Logins = append(index.Logins, indexedLogin{APIOrigin: login.APIOrigin, Organization: login.Organization, Name: login.OrganizationName})
	}
	// The index contains no tokens. Stage it before changing credential entries.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return credentialStoreError()
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".logins-*")
	if err != nil {
		return credentialStoreError()
	}
	defer os.Remove(file.Name())
	writeErr := json.NewEncoder(file).Encode(index)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return credentialStoreError()
	}
	for _, login := range saved.Logins {
		if login.AccessToken == "" {
			continue
		}
		value, err := json.Marshal(login)
		if err != nil {
			return credentialStoreError()
		}
		stored, err := keyring.Get(credentialService, loginAccount(reference(login)))
		if err == nil && stored == string(value) {
			continue
		}
		if err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return credentialStoreError()
		}
		if err := keyring.Set(credentialService, loginAccount(reference(login)), string(value)); err != nil {
			return credentialStoreError()
		}
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return credentialStoreError()
	}
	// Remove credentials only after the index no longer refers to them.
	for _, login := range previous.Logins {
		if _, retained := saved.find(reference(login)); retained {
			continue
		}
		err := keyring.Delete(credentialService, loginAccount(reference(login)))
		if err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return credentialStoreError()
		}
	}
	// The old copy is no longer read once the organization index exists.
	_ = keyring.Delete(credentialService, credentialAccount)
	return nil
}

func (store CredentialStore) indexPath() (string, error) {
	directory := store.Directory
	if directory == "" {
		configuration, err := os.UserConfigDir()
		if err != nil {
			return "", credentialStoreError()
		}
		directory = filepath.Join(configuration, "zorveus")
	}
	return filepath.Join(directory, "logins.json"), nil
}

func loginAccount(target LoginReference) string {
	value, _ := json.Marshal(target)
	return fmt.Sprintf("login-%x", sha256.Sum256(value))
}

func invalidSavedLogins() error {
	return problem.New("invalid_saved_login", "The saved logins are unreadable. Check the Zorveus login index and com.zorveus.cli entries in your OS credential store.", 3)
}

func credentialStoreError() error {
	return problem.New("credential_store_unavailable", "Could not read or save CLI login details. Unlock your OS credential store and check your user configuration directory permissions. Tokens are never saved as plaintext.", 1)
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
