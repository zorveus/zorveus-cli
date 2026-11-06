package provision

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/manifest"
)

func serviceKeyActive(key api.ServiceKey, now time.Time) bool {
	return key.Status == "active" && (key.ExpiresAt == nil || key.ExpiresAt.After(now))
}

func validateProviderConfig(provider string, raw json.RawMessage, index int) error {
	var config map[string]any
	_ = json.Unmarshal(raw, &config)
	return manifest.ValidateProviderConfig(provider, config, "/providerCredentials/"+strconv.Itoa(index)+"/providerConfig")
}

func (run *execution) updateServiceMetadata(step Step) error {
	id := run.id(step.Ref)
	path := "/orgs/" + url.PathEscape(run.plan.Target.Org) + "/service-keys/" + url.PathEscape(id)
	var updated api.ServiceKey
	// Status and prefix guard the plan, but are not editable request fields.
	payload := step.After.only("key_alias", "scopes")
	if len(payload) == 0 {
		return conflict()
	}
	if err := run.write(step, http.MethodPatch, path, payload, &updated); err != nil {
		return err
	}
	if updated.ID != id || updated.Organization != run.plan.Target.Org || !serviceKeyActive(updated, run.remote.service.Auth.Now()) {
		return conflict()
	}
	if !sameFields(project(fields(updated), step.After), step.After) {
		return conflict()
	}
	return nil
}

func (run *execution) updateProviderMetadata(step Step) error {
	current, err := run.remote.provider(run.id(step.Ref))
	if err != nil {
		return err
	}
	if fields(current).text("active_secret_version_id") != step.VersionID || !sameFields(project(fields(current), step.After), step.Before) {
		return conflict()
	}
	payload := step.After.only("credential_name", "routing_mode", "default_model_policy", "provider_config")
	if len(payload) == 0 {
		return conflict()
	}
	path := "/provider-credentials/" + url.PathEscape(current.ID) + "?" + url.Values{"org_id": {run.plan.Target.Org}}.Encode()
	var updated api.ProviderCredential
	if err := run.write(step, http.MethodPatch, path, payload, &updated); err != nil {
		return err
	}
	if updated.ID != current.ID || updated.Organization != run.plan.Target.Org || updated.Provider != current.Provider || fields(updated).text("active_secret_version_id") != step.VersionID {
		return conflict()
	}
	if !sameFields(project(fields(updated), step.After), step.After) {
		return conflict()
	}
	return nil
}
