package provision

import (
	"os"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func (service *Service) secretStore() SecretStore {
	if service.Secrets != nil {
		return service.Secrets
	}
	return CredentialStore{}
}

func providerAccount(target Target, id string) string {
	return "provider/" + digest([]byte(target.Origin+"\n"+target.Org)) + "/" + id
}

func providerFingerprint(value, kind string) string {
	// Match the backend's whitespace normalization, without storing the input.
	return digest([]byte(kind + "\n" + strings.TrimSpace(value)))
}

func (remote *gateway) providerInputChanged(current api.ProviderCredential, variable, kind string) (bool, error) {
	value, exists := os.LookupEnv(variable)
	if !exists || strings.TrimSpace(value) == "" {
		return true, nil
	}
	if workspace := remote.service.workspace; workspace != nil && current.VersionID != nil {
		if previous, found := workspace.snapshot.ProviderFingerprints[current.ID]; found {
			return previous.VersionID != *current.VersionID || previous.Fingerprint != providerFingerprint(value, kind), nil
		}
	}
	saved, err := remote.service.secretStore().Load(providerAccount(remote.target, current.ID))
	if secretMissing(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if current.VersionID == nil {
		return true, nil
	}
	return saved.ResourceID != current.ID || saved.CredentialID != *current.VersionID || saved.Secret != providerFingerprint(value, kind), nil
}

func stepUsesSecretStore(step Step) bool {
	return stepHasCredential(step) || step.Kind == "create_provider" || step.Kind == "provider_secret"
}

func (run *execution) prepareProviderInputs() error {
	for _, step := range run.plan.Steps {
		if step.SecretEnv == "" {
			continue
		}
		account := run.plan.ID + "/" + step.Ref + "/input"
		fingerprint := providerFingerprint(run.inputs[step.SecretEnv], step.SecretKind)
		if workspace := run.remote.service.workspace; workspace != nil {
			if workspace.snapshot.InputFingerprints == nil {
				workspace.snapshot.InputFingerprints = map[string]string{}
			}
			if previous, exists := workspace.snapshot.InputFingerprints[account]; exists && previous != fingerprint {
				return problem.New("secret_input_changed", "A provider secret input changed during this durable apply. Restore the original environment value before resuming.", 4)
			}
			workspace.snapshot.InputFingerprints[account] = fingerprint
		}
		saved, err := run.secrets.Load(account)
		if err == nil {
			if saved.ResourceID != step.Ref || saved.Secret != fingerprint {
				return problem.New("secret_input_changed", "A provider secret input changed during this apply. Restore the original environment value before resuming the saved plan.", 4)
			}
			continue
		}
		if !secretMissing(err) {
			return err
		}
		if err := run.secrets.Save(account, Credential{ResourceID: step.Ref, Secret: fingerprint}); err != nil {
			return err
		}
	}
	return nil
}

func (run *execution) rememberProvider(step Step, id, version string) error {
	if version == "" {
		return incomplete()
	}
	current, err := run.remote.provider(id)
	if err != nil {
		return err
	}
	if current.VersionID == nil || *current.VersionID != version || current.Status != "active" {
		return conflict()
	}
	saved, err := run.secrets.Load(run.plan.ID + "/" + step.Ref + "/input")
	if err != nil {
		return err
	}
	if err := run.secrets.Save(providerAccount(run.plan.Target, id), Credential{ResourceID: id, CredentialID: version, Secret: saved.Secret}); err != nil {
		return err
	}
	if workspace := run.remote.service.workspace; workspace != nil {
		if workspace.snapshot.ProviderFingerprints == nil {
			workspace.snapshot.ProviderFingerprints = map[string]providerFingerprintRecord{}
		}
		workspace.snapshot.ProviderFingerprints[id] = providerFingerprintRecord{VersionID: version, Fingerprint: saved.Secret}
	}
	return nil
}
