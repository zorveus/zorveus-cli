package provision

import (
	"net/http"
	"net/url"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

type createdResource struct {
	App           api.ManagedApp    `json:"app"`
	Connection    api.AppConnection `json:"app_connection"`
	Router        api.RoutingKey    `json:"routing_key"`
	KeyID         string            `json:"api_key_id"`
	NewKeyID      string            `json:"new_api_key_id"`
	AppID         string            `json:"app_id"`
	APIKey        string            `json:"api_key"`
	ClientSecret  string            `json:"client_secret"`
	ServiceID     string            `json:"org_service_api_key_id"`
	Organization  string            `json:"org_id"`
	ServiceSecret string            `json:"service_key"`
	ProviderID    string            `json:"provider_credential_id"`
	VersionID     string            `json:"active_secret_version_id"`
	TokenPrefix   string            `json:"token_prefix"`
}

func (run *execution) create(index int, step Step) error {
	progress := &run.journal.Steps[index]
	account := run.plan.ID + "/" + step.Ref
	if progress.ID != "" {
		return run.resumeCreation(index, step, account)
	}
	// A request may have succeeded before the connection was lost. Without a
	// returned ID, repeating it could create a second resource.
	if progress.Started {
		return incomplete()
	}
	method, path, payload, err := run.creationRequest(step)
	if err != nil {
		return err
	}
	progress.Started = true
	if err := run.save(); err != nil {
		return err
	}
	var response createdResource
	requestErr := run.write(step, method, path, payload, &response)
	id, credentialID, secret := run.createdIdentity(step, response)
	if id == "" {
		if requestErr != nil {
			code := problem.Safe(requestErr).Code
			if code == "login_required" || code == "authorization_required" || code == "access_denied" {
				progress.Started = false
				if err := run.save(); err != nil {
					return err
				}
			}
			return requestErr
		}
		return incomplete()
	}
	// Persist the identity before delivering the one-time secret. If storage
	// fails, the next attempt can rotate this exact resource.
	progress.ID, progress.CredentialID = id, credentialID
	if step.Kind == "create_service" {
		progress.SecretPrefix = response.TokenPrefix
	}
	if err := run.save(); err != nil {
		return err
	}
	if requestErr != nil {
		return requestErr
	}
	if secret == "" && stepHasCredential(step) {
		return incomplete()
	}
	if stepHasCredential(step) {
		if err := run.secrets.Save(account, Credential{id, credentialID, secret}); err != nil {
			return err
		}
	}
	current, _, err := run.current(step)
	if err != nil {
		return err
	}
	if !sameFields(current, run.after(step)) {
		return conflict()
	}
	if step.Kind == "create_provider" {
		if err := run.rememberProvider(step, id, credentialID); err != nil {
			return err
		}
	}
	progress.Done = true
	return run.save()
}

func (run *execution) creationRequest(step Step) (string, string, Fields, error) {
	payload := Fields{}
	for name, value := range step.After {
		payload[name] = value
	}
	switch step.Kind {
	case "create_app":
		payload.set("owner_org_id", run.plan.Target.Org)
		return http.MethodPost, "/apps", payload, nil
	case "create_key":
		payload.set("org_id", run.plan.Target.Org)
		payload.set("app_id", run.id("app"))
		return http.MethodPost, "/app-connections/dashboard", payload, nil
	case "create_router":
		payload, err := run.routingPayload(step)
		return http.MethodPost, run.routingPath(""), payload, err
	case "create_service":
		return http.MethodPost, "/orgs/" + url.PathEscape(run.plan.Target.Org) + "/service-keys", payload, nil
	case "create_provider":
		payload.set("secret", run.inputs[step.SecretEnv])
		payload.set("secret_kind", step.SecretKind)
		return http.MethodPost, "/provider-credentials?" + url.Values{"org_id": {run.plan.Target.Org}}.Encode(), payload, nil
	default:
		return "", "", nil, conflict()
	}
}

func (run *execution) createdIdentity(step Step, response createdResource) (string, string, string) {
	switch step.Kind {
	case "create_app":
		if response.App.Organization == run.plan.Target.Org && resourceID.MatchString(response.App.ID) {
			return response.App.ID, response.App.ID, response.ClientSecret
		}
	case "create_key":
		connection := response.Connection
		keyID := response.KeyID
		if response.NewKeyID != "" {
			keyID = response.NewKeyID
		}
		if connection.Organization == run.plan.Target.Org && connection.AppID == run.id("app") && connection.Type == "org_programmatic" && resourceID.MatchString(connection.ID) && resourceID.MatchString(keyID) {
			return connection.ID, keyID, response.APIKey
		}
	case "create_router":
		if response.Router.Organization == run.plan.Target.Org && response.Router.AppID == run.id("app") && resourceID.MatchString(response.Router.KeyID) {
			return response.Router.KeyID, response.Router.KeyID, response.APIKey
		}
	case "create_service":
		if response.Organization == run.plan.Target.Org && resourceID.MatchString(response.ServiceID) && response.TokenPrefix != "" {
			return response.ServiceID, response.ServiceID, response.ServiceSecret
		}
	case "create_provider":
		if response.Organization == run.plan.Target.Org && resourceID.MatchString(response.ProviderID) {
			return response.ProviderID, response.VersionID, ""
		}
	}
	return "", "", ""
}

func (run *execution) resumeCreation(index int, step Step, account string) error {
	progress := &run.journal.Steps[index]
	if step.Kind == "create_service" && progress.RotationStarted && !progress.RotationReceived {
		return unknownServiceRotation()
	}
	current, _, err := run.current(step)
	if err != nil {
		return err
	}
	if !sameFields(current, run.after(step)) {
		return conflict()
	}
	if step.Kind == "create_provider" {
		current, err := run.remote.provider(progress.ID)
		if err != nil {
			return err
		}
		if current.VersionID == nil || *current.VersionID != progress.CredentialID {
			return conflict()
		}
		if err := run.rememberProvider(step, progress.ID, progress.CredentialID); err != nil {
			return err
		}
	}
	if progress.Done || !stepHasCredential(step) {
		progress.Done = true
		return run.save()
	}
	credential, err := run.secrets.Load(account)
	if err == nil {
		if credential.ResourceID != progress.ID || credential.CredentialID != progress.CredentialID {
			return conflict()
		}
		if step.Kind == "create_service" {
			current, err := run.remote.serviceKey(progress.ID)
			if err != nil {
				return err
			}
			if progress.SecretPrefix != "" && current.TokenPrefix != progress.SecretPrefix {
				return conflict()
			}
		}
		progress.Done = true
		return run.save()
	}
	if !secretMissing(err) {
		return err
	}
	return run.recoverCredential(index, step, account)
}

func (run *execution) recoverCredential(index int, step Step, account string) error {
	if step.Kind == "create_service" {
		return run.recoverServiceCredential(index, step, account)
	}
	progress := &run.journal.Steps[index]
	path := ""
	switch step.Kind {
	case "create_app":
		path = "/apps/" + url.PathEscape(progress.ID) + "/client-secret/rotate"
	case "create_key":
		path = run.connectionPath(step.Ref) + "/keys/rotate"
	case "create_router":
		path = run.routingPath(step.Ref) + "/rotate"
	}
	var response createdResource
	requestErr := run.write(step, http.MethodPost, path, nil, &response)
	id, credentialID, secret := run.createdIdentity(step, response)
	if step.Kind == "create_app" && response.AppID == progress.ID {
		id, credentialID, secret = response.AppID, response.AppID, response.ClientSecret
	}
	if id == "" {
		if requestErr != nil {
			return requestErr
		}
		return incomplete()
	}
	if step.Kind != "create_router" && id != progress.ID {
		return conflict()
	}
	progress.ID, progress.CredentialID = id, credentialID
	if err := run.save(); err != nil {
		return err
	}
	if requestErr != nil {
		return requestErr
	}
	if secret == "" {
		return incomplete()
	}
	if err := run.secrets.Save(account, Credential{id, credentialID, secret}); err != nil {
		return err
	}
	progress.Done = true
	return run.save()
}
