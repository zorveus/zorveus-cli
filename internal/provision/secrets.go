package provision

import (
	"encoding/json"
	"errors"

	"github.com/zalando/go-keyring"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

var ErrSecretMissing = errors.New("credential not saved")

type Credential struct {
	ResourceID   string `json:"resource_id"`
	CredentialID string `json:"credential_id"`
	Secret       string `json:"-"`
}

type SecretStore interface {
	Check(string) error
	Load(string) (Credential, error)
	Save(string, Credential) error
}

type CredentialStore struct{}

const resourceService = "com.zorveus.cli.resources"

func (CredentialStore) Check(account string) error {
	probe := account + "/check"
	if err := keyring.Set(resourceService, probe, "credential-store-check"); err != nil {
		return secretStoreError()
	}
	value, err := keyring.Get(resourceService, probe)
	deleteErr := keyring.Delete(resourceService, probe)
	if err != nil || deleteErr != nil || value != "credential-store-check" {
		return secretStoreError()
	}
	return nil
}

func (CredentialStore) Load(account string) (Credential, error) {
	value, err := keyring.Get(resourceService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return Credential{}, ErrSecretMissing
	}
	if err != nil {
		return Credential{}, secretStoreError()
	}
	var wire struct {
		ResourceID   string `json:"resource_id"`
		CredentialID string `json:"credential_id"`
		Secret       string `json:"secret"`
	}
	if json.Unmarshal([]byte(value), &wire) != nil || wire.Secret == "" {
		return Credential{}, secretStoreError()
	}
	return Credential{ResourceID: wire.ResourceID, CredentialID: wire.CredentialID, Secret: wire.Secret}, nil
}

func (CredentialStore) Save(account string, value Credential) error {
	wire := struct {
		ResourceID   string `json:"resource_id"`
		CredentialID string `json:"credential_id"`
		Secret       string `json:"secret"`
	}{value.ResourceID, value.CredentialID, value.Secret}
	data, err := json.Marshal(wire)
	if err != nil {
		return secretStoreError()
	}
	if err := keyring.Set(resourceService, account, string(data)); err != nil {
		return secretStoreError()
	}
	return nil
}

func secretStoreError() error {
	return problem.New("credential_store_unavailable", "Could not securely save resource credentials. Unlock the OS credential store and retry the same saved plan.", 1)
}
