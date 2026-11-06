package provision

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
)

func (run *execution) write(step Step, method, path string, payload, response any) error {
	if strings.HasPrefix(path, "/app-connections/") && path != "/app-connections/dashboard" {
		path += "?" + url.Values{"org_id": {run.plan.Target.Org}}.Encode()
	}
	return run.remote.call(stepScopes(step), func(client *api.Client, login auth.Login) error {
		return client.WriteJSON(run.remote.ctx, login.APIOrigin, method, path, login.AccessToken, payload, response)
	})
}

func (run *execution) connectionPath(ref string) string {
	return "/app-connections/" + url.PathEscape(run.id(ref))
}

func (run *execution) routingPath(ref string) string {
	path := "/orgs/" + url.PathEscape(run.plan.Target.Org) + "/apps/" + url.PathEscape(run.id("app")) + "/routing-keys"
	if ref != "" {
		path += "/" + url.PathEscape(run.id(ref))
	}
	return path
}

// Membership selectors refer to credentials. Read them after all connection
// writes, since rotating an inference key preserves its connection ID.
func (run *execution) routingPayload(step Step) (Fields, error) {
	payload := Fields{}
	for name, value := range step.After {
		payload[name] = value
	}
	members := make([]Fields, 0, len(step.Members))
	for _, member := range step.Members {
		connection, err := run.remote.key(run.id("app"), run.id(member.Ref))
		if err != nil {
			return nil, err
		}
		if connection.credential == nil || connection.credential.Status != "active" {
			return nil, conflict()
		}
		members = append(members, fields(map[string]string{
			"selected_api_key_id": connection.credential.ID,
			"selector_alias":      member.Alias,
		}))
	}
	payload.set("members", members)
	return payload, nil
}

func (run *execution) update(step Step, capID string) error {
	if step.Kind == "service_metadata" {
		return run.updateServiceMetadata(step)
	}
	if step.Kind == "provider_metadata" {
		return run.updateProviderMetadata(step)
	}
	method := http.MethodPatch
	path := run.connectionPath(step.Ref)
	payload := step.After
	switch step.Kind {
	case "app_profile":
		path = "/apps/" + url.PathEscape(run.id("app"))
	case "app_models":
		path = "/apps/" + url.PathEscape(run.id("app")) + "/models"
	case "app_redirect_add":
		method, path = http.MethodPost, "/apps/"+url.PathEscape(run.id("app"))+"/redirect-uris"
		payload = step.After.pick("redirect_uri")
	case "app_redirect_remove":
		if !resourceID.MatchString(step.ID) {
			return conflict()
		}
		method, path, payload = http.MethodDelete, "/apps/"+url.PathEscape(run.id("app"))+"/redirect-uris/"+url.PathEscape(step.ID), nil
	case "key_policy":
		connection, err := run.remote.key(run.id("app"), run.id(step.Ref))
		if err != nil {
			return err
		}
		if connection.credential == nil {
			return conflict()
		}
		path += "/keys/" + url.PathEscape(connection.credential.ID)
	case "key_credit":
		path += "/credit-mode"
	case "key_overrun":
		path += "/overrun-policy"
	case "key_pricing":
		path += "/product-user-pricing"
	case "key_cap":
		path += "/cap"
		if step.After == nil {
			method = http.MethodDelete
			path = "/caps/" + url.PathEscape(capID)
		}
	case "router_name":
		path = run.routingPath(step.Ref)
	case "router_members":
		method = http.MethodPut
		path = run.routingPath(step.Ref) + "/members"
		var err error
		payload, err = run.routingPayload(step)
		if err != nil {
			return err
		}
	case "revoke_key":
		method, path, payload = http.MethodPost, path+"/revoke", nil
	case "revoke_router":
		method, path, payload = http.MethodPost, run.routingPath(step.Ref)+"/revoke", nil
	default:
		return conflict()
	}
	// An absent payload must stay absent, rather than encode a nil map as JSON null.
	if payload == nil {
		return run.write(step, method, path, nil, nil)
	}
	return run.write(step, method, path, payload, nil)
}
