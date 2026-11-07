package provision

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func (remote *gateway) serviceKeys() ([]api.ServiceKey, error) {
	var result []api.ServiceKey
	err := remote.call([]string{"service_keys:read"}, func(client *api.Client, login auth.Login) error {
		var err error
		result, err = client.ListServiceKeys(remote.ctx, login.APIOrigin, login.Organization, login.AccessToken)
		return err
	})
	return result, err
}

func (remote *gateway) serviceKey(id string) (api.ServiceKey, error) {
	keys, err := remote.serviceKeys()
	if err != nil {
		return api.ServiceKey{}, err
	}
	for _, key := range keys {
		if key.ID == id {
			return key, nil
		}
	}
	return api.ServiceKey{}, problem.New("resource_not_found", "The service key was not found in the approved organization.", 1)
}

func (remote *gateway) provider(id string) (api.ProviderCredential, error) {
	var credentials []api.ProviderCredential
	err := remote.call([]string{"provider_credentials:read"}, func(client *api.Client, login auth.Login) error {
		var err error
		credentials, err = client.ListProviderCredentials(remote.ctx, login.APIOrigin, login.Organization, login.AccessToken)
		return err
	})
	if err != nil {
		return api.ProviderCredential{}, err
	}
	for _, credential := range credentials {
		if credential.ID == id {
			return credential, nil
		}
	}
	return api.ProviderCredential{}, problem.New("resource_not_found", "The provider credential was not found in the approved organization.", 1)
}

func (plan *Plan) planShared(remote *gateway, desired Manifest) error {
	for index, key := range desired.ServiceKeys {
		ref, id := desired.resourceRef("serviceKeys", index), key.text("id")
		plan.Bindings[ref] = id
		payload := Fields{}
		if key.has("name") {
			payload["key_alias"] = key["name"]
		}
		if key.has("scopes") {
			payload["scopes"] = key["scopes"]
		}
		if id == "" {
			plan.Steps = append(plan.Steps, Step{Kind: "create_service", Ref: ref, After: payload})
			continue
		}
		current, err := remote.serviceKey(id)
		if err != nil {
			return err
		}
		if !serviceKeyActive(current, remote.service.Auth.Now()) {
			return conflict()
		}
		payload.set("status", "active")
		payload.set("token_prefix", current.TokenPrefix)
		plan.add("service_metadata", ref, id, fields(current), payload)
	}
	for index, credential := range desired.Providers {
		if err := plan.planProvider(remote, desired.resourceRef("providerCredentials", index), desired.resourcePointer("providerCredentials", index), credential); err != nil {
			return err
		}
	}
	return nil
}

func (plan *Plan) planProvider(remote *gateway, ref, pointer string, credential Fields) error {
	id := credential.text("id")
	plan.Bindings[ref] = id
	payload := providerMetadata(credential)
	kind := providerSecretKind(credential)
	if id == "" {
		if !payload.has("routing_mode") {
			payload.set("routing_mode", "auto_resolve")
		}
		if !payload.has("default_model_policy") {
			payload.set("default_model_policy", []string{})
		}
		plan.Steps = append(plan.Steps, Step{Kind: "create_provider", Ref: ref, After: payload, SecretEnv: credential.text("secretFromEnv"), SecretKind: kind})
		return nil
	}
	current, err := remote.provider(id)
	if err != nil {
		return err
	}
	if current.Status != "active" {
		return conflict()
	}
	if credential.has("provider") && credential.text("provider") != current.Provider {
		return manifestError("immutable_provider", "A credential's provider is a creation-only setting. Declare a new resource to use another provider.", pointer+"/provider")
	}
	if credential.has("providerConfig") {
		if err := validateProviderConfig(current.Provider, credential["providerConfig"], pointer); err != nil {
			return err
		}
	}
	payload.set("provider", current.Provider)
	payload.set("status", "active")
	plan.add("provider_metadata", ref, id, fields(current), payload)
	plan.Steps[len(plan.Steps)-1].VersionID = fields(current).text("active_secret_version_id")
	if credential.has("secretFromEnv") {
		changed, err := remote.providerInputChanged(current, credential.text("secretFromEnv"), kind)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		if current.VersionID == nil || *current.VersionID == "" {
			return problem.New("invalid_response", "The provider credential has no active version ID for rotation planning.", 1)
		}
		plan.Steps = append(plan.Steps, Step{Kind: "provider_secret", Ref: ref, ID: id,
			Before: fields(current).pick("active_secret_version_id"), After: fields(map[string]bool{"rotation_requested": true}), SecretEnv: credential.text("secretFromEnv"), SecretKind: kind})
	}
	return nil
}

func readSecretInputs(plan Plan) (map[string]string, error) {
	inputs := map[string]string{}
	for _, step := range plan.Steps {
		if step.SecretEnv == "" {
			continue
		}
		value, exists := os.LookupEnv(step.SecretEnv)
		if !exists || strings.TrimSpace(value) == "" || len(value) > 1<<20 {
			return nil, problem.New("secret_input_required", "Set the environment variable named by secretFromEnv before applying. Provider secret inputs must be nonempty and at most 1 MiB.", 2)
		}
		inputs[step.SecretEnv] = value
	}
	return inputs, nil
}

func (run *execution) rotateProvider(index int, step Step) error {
	progress := &run.journal.Steps[index]
	current, err := run.remote.provider(run.id(step.Ref))
	if err != nil {
		return err
	}
	if current.Status != "active" || current.VersionID == nil {
		return conflict()
	}
	if progress.CredentialID != "" {
		if *current.VersionID != progress.CredentialID {
			return conflict()
		}
		if err := run.rememberProvider(step, current.ID, progress.CredentialID); err != nil {
			return err
		}
		progress.Done = true
		return run.save()
	}
	if !sameFields(fields(current).pick("active_secret_version_id"), step.Before) {
		if progress.Started {
			return incomplete()
		}
		return conflict()
	}
	progress.Started = true
	if err := run.save(); err != nil {
		return err
	}
	var response struct {
		Credential api.ProviderCredential `json:"provider_credential"`
	}
	path := "/provider-credentials/" + url.PathEscape(current.ID) + "/rotate?" + url.Values{"org_id": {run.plan.Target.Org}}.Encode()
	// The source value exists only in memory and the authenticated request.
	payload := map[string]string{"secret": run.inputs[step.SecretEnv], "secret_kind": step.SecretKind}
	if err := run.write(step, http.MethodPost, path, payload, &response); err != nil {
		return err
	}
	if response.Credential.ID != current.ID || response.Credential.Organization != run.plan.Target.Org || response.Credential.VersionID == nil || *response.Credential.VersionID == *current.VersionID {
		return incomplete()
	}
	progress.CredentialID = *response.Credential.VersionID
	if err := run.save(); err != nil {
		return err
	}
	verified, err := run.remote.provider(current.ID)
	if err != nil {
		return err
	}
	if verified.VersionID == nil || *verified.VersionID != progress.CredentialID {
		return conflict()
	}
	if err := run.rememberProvider(step, current.ID, progress.CredentialID); err != nil {
		return err
	}
	progress.Done = true
	return run.save()
}

func providerMetadata(credential Fields) Fields {
	payload := Fields{}
	for source, destination := range map[string]string{"name": "credential_name", "provider": "provider", "routingMode": "routing_mode", "defaultModelPolicy": "default_model_policy", "providerConfig": "provider_config"} {
		if value, exists := credential[source]; exists {
			payload[destination] = value
		}
	}
	return payload
}

func providerSecretKind(credential Fields) string {
	kind := credential.text("secretKind")
	if kind == "" {
		return "api_key"
	}
	return kind
}
