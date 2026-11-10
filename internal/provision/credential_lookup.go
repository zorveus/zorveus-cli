package provision

import (
	"os"
	"path/filepath"
	"strings"
)

// Resource identity keeps credentials reachable after aliases and plan files change.
// The operation account remains available for interrupted-apply recovery.
func credentialAccount(target Target, kind, id string) string {
	identity := []string{target.Origin, target.Org, target.User, kind, id}
	return "resource/" + digest(encoded(identity))
}

func (service *Service) rememberCredentials(plan Plan, journal Journal, onlyMissing bool) error {
	store := service.secretStore()
	for index, step := range plan.Steps {
		if !stepHasCredential(step) || !journal.Steps[index].Done {
			continue
		}
		progress := journal.Steps[index]
		kind := strings.TrimPrefix(step.Kind, "create_")
		account := credentialAccount(plan.Target, kind, progress.ID)
		if onlyMissing {
			if _, err := store.Load(account); err == nil {
				continue
			} else if !secretMissing(err) {
				return err
			}
		}
		credential, err := store.Load(plan.ID + "/" + step.Ref)
		if secretMissing(err) {
			continue
		}
		if err != nil {
			return err
		}
		if credential.ResourceID != progress.ID || credential.CredentialID != progress.CredentialID {
			return conflict()
		}
		credential.TokenPrefix = progress.SecretPrefix
		if err := store.Save(account, credential); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) resourceCredential(config string, target Target, kind, ref string, expected Credential) (Credential, error) {
	credential, err := service.secretStore().Load(credentialAccount(target, kind, expected.ResourceID))
	if secretMissing(err) {
		credential, err = service.legacyCredential(config, target, kind, ref, expected)
	}
	if err != nil {
		return Credential{}, err
	}
	if credential.ResourceID != expected.ResourceID || credential.CredentialID != expected.CredentialID || credential.Secret == "" || expected.TokenPrefix != "" && credential.TokenPrefix != expected.TokenPrefix {
		return Credential{}, staleResourceCredential()
	}
	return credential, nil
}

// Older releases saved only operation accounts. Existing journals let us find
// those credentials without asking the backend for another secret or rotating.
func (service *Service) legacyCredential(config string, target Target, kind, ref string, expected Credential) (Credential, error) {
	entries, err := os.ReadDir(directory(config))
	if err != nil {
		return Credential{}, storageError()
	}
	for _, entry := range entries {
		operation := strings.TrimSuffix(entry.Name(), ".apply.json")
		if operation == entry.Name() || !operationID.MatchString(operation) {
			continue
		}
		var journal Journal
		if _, _, err := loadJSON(filepath.Join(directory(config), entry.Name()), &journal); err != nil {
			return Credential{}, err
		}
		if !journal.Complete || journal.Config != config {
			continue
		}
		for _, progress := range journal.Steps {
			if !progress.Done || progress.ID != expected.ResourceID || progress.CredentialID != expected.CredentialID || progress.SecretPrefix != expected.TokenPrefix {
				continue
			}
			credential, err := service.secretStore().Load(operation + "/" + ref)
			if secretMissing(err) {
				continue
			}
			if err != nil {
				return Credential{}, err
			}
			if credential.ResourceID != expected.ResourceID || credential.CredentialID != expected.CredentialID || credential.Secret == "" {
				return Credential{}, staleResourceCredential()
			}
			credential.TokenPrefix = expected.TokenPrefix
			if err := service.secretStore().Save(credentialAccount(target, kind, expected.ResourceID), credential); err != nil {
				return Credential{}, err
			}
			return credential, nil
		}
	}
	return Credential{}, missingResourceCredential()
}
