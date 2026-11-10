package provision

import (
	"context"
	"path/filepath"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

// RetrievedSecret is returned only by the explicit retrieve command.
type RetrievedSecret struct {
	Item   string `json:"item"`
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

type secretResource struct {
	item, kind, ref, id string
}

func (service *Service) Retrieve(ctx context.Context, origin, config, item string) (RetrievedSecret, error) {
	config, err := filepath.Abs(config)
	if err != nil {
		return RetrievedSecret{}, storageError()
	}
	unlock, err := lockConfig(ctx, config)
	if err != nil {
		return RetrievedSecret{}, err
	}
	defer unlock()
	if err := requireFinishedApply(config); err != nil {
		return RetrievedSecret{}, err
	}
	desired, _, _, err := loadManifest(config)
	if err != nil {
		return RetrievedSecret{}, err
	}
	selected, err := desired.secretResource(item)
	if err != nil {
		return RetrievedSecret{}, err
	}
	if selected.kind == "provider" {
		return RetrievedSecret{}, problem.New("secret_not_available", "Provider secrets come from secretFromEnv and are not saved by the CLI. Retrieve that value from your original secret store.", 2)
	}
	appID := desired.App.text("id")
	if selected.id == "" || selected.kind != "service" && appID == "" {
		return RetrievedSecret{}, problem.New("apply_required", "This item has no applied resource ID. Review and apply its plan before retrieving its secret.", 2)
	}
	remote := &gateway{ctx: ctx, service: service, target: Target{Origin: origin}}
	if err := remote.identify(); err != nil {
		return RetrievedSecret{}, err
	}
	state, _, err := readState(config, remote.target, appID)
	if err != nil {
		return RetrievedSecret{}, err
	}
	if state.Target.User != "" && state.Target.User != remote.target.User {
		return RetrievedSecret{}, problem.New("credential_target_mismatch", "This configuration was applied by another CLI user. Sign in with that user to retrieve its saved secrets.", 4)
	}
	expected, err := service.secretIdentity(remote, appID, selected)
	if err != nil {
		return RetrievedSecret{}, err
	}
	credential, err := service.resourceCredential(config, remote.target, selected.kind, selected.ref, expected)
	if err != nil {
		return RetrievedSecret{}, err
	}
	return RetrievedSecret{Item: selected.item, ID: selected.id, Secret: credential.Secret}, nil
}

func (desired Manifest) secretResource(item string) (secretResource, error) {
	matches := []secretResource{}
	appID := desired.App.text("id")
	if item == "app" || appID != "" && item == appID {
		matches = append(matches, secretResource{item: "app", kind: "app", ref: "app", id: appID})
	}
	sections := []struct {
		name, kind string
		resources  []Fields
	}{
		{"keys", "key", desired.Keys},
		{"routingKeys", "router", desired.Routers},
		{"serviceKeys", "service", desired.ServiceKeys},
		{"providerCredentials", "provider", desired.Providers},
	}
	for _, section := range sections {
		for index, resource := range section.resources {
			name := section.name + "." + desired.resourceNames[section.name][index]
			id := resource.text("id")
			if item == name || id != "" && item == id {
				matches = append(matches, secretResource{item: name, kind: section.kind, ref: desired.resourceRef(section.name, index), id: id})
			}
		}
	}
	if len(matches) == 0 {
		return secretResource{}, problem.New("item_not_found", "The item or ID is not in this manifest. Use app, keys.<name>, routingKeys.<name>, or serviceKeys.<name>, with --config for another file.", 2)
	}
	if len(matches) > 1 {
		return secretResource{}, problem.New("ambiguous_item", "This ID matches more than one manifest item. Select its full reference, such as keys.plus.", 2)
	}
	return matches[0], nil
}

func (service *Service) secretIdentity(remote *gateway, appID string, selected secretResource) (Credential, error) {
	expected := Credential{ResourceID: selected.id, CredentialID: selected.id}
	if selected.kind == "service" {
		key, err := remote.serviceKey(selected.id)
		if err != nil {
			return Credential{}, err
		}
		if !serviceKeyActive(key, service.Auth.Now()) || key.TokenPrefix == "" {
			return Credential{}, staleResourceCredential()
		}
		expected.TokenPrefix = key.TokenPrefix
		return expected, nil
	}
	app, err := remote.app(appID)
	if err != nil {
		return Credential{}, err
	}
	switch selected.kind {
	case "app":
		if app.ClientType == "public" {
			return Credential{}, problem.New("secret_not_available", "Public apps do not have a client secret.", 2)
		}
	case "key":
		key, err := remote.key(appID, selected.id)
		if err != nil {
			return Credential{}, err
		}
		active := key.credential
		if key.detail.Connection.Status != "active" || active == nil || active.Status != "active" || active.ExpiresAt != nil && !active.ExpiresAt.After(service.Auth.Now()) {
			return Credential{}, staleResourceCredential()
		}
		expected.CredentialID = active.ID
	case "router":
		key, err := remote.router(appID, selected.id)
		if err != nil {
			return Credential{}, err
		}
		if key.Status != "active" {
			return Credential{}, staleResourceCredential()
		}
	default:
		return Credential{}, problem.New("secret_not_available", "This item does not have a saved secret.", 2)
	}
	return expected, nil
}

func missingResourceCredential() error {
	return problem.New("resource_credential_missing", "This secret is not saved in your OS credential store. Restore its saved entry or explicitly recover or rotate the credential. An ID alone cannot recover the original secret; retrieve never rotates it.", 2)
}

func staleResourceCredential() error {
	return problem.New("resource_credential_stale", "The credential is expired, revoked, or no longer matches its saved value. Recover it explicitly before retrieving; retrieve never rotates it.", 2)
}
