package provision

import (
	"context"
	"slices"
	"sort"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

type Service struct {
	Auth    *auth.Service
	Secrets SecretStore
}

type gateway struct {
	ctx     context.Context
	service *Service
	target  Target
}

func (remote *gateway) call(scopes []string, run func(*api.Client, auth.Login) error) error {
	target := auth.LoginTarget{APIOrigin: remote.target.Origin, Organization: remote.target.Org, UserID: remote.target.User}
	return remote.service.Auth.UseLogin(remote.ctx, target, scopes, func(login auth.Login) error {
		return run(remote.service.Auth.API, login)
	})
}

func (remote *gateway) identify() error {
	return remote.call(nil, func(client *api.Client, login auth.Login) error {
		me, err := client.GetMe(remote.ctx, login.APIOrigin, login.Organization, login.AccessToken)
		if err != nil {
			return err
		}
		if remote.target.User != "" && me.User.ID != remote.target.User {
			return conflict()
		}
		remote.target = Target{Origin: login.APIOrigin, Org: login.Organization, User: me.User.ID}
		return nil
	})
}

func (remote *gateway) app(id string) (api.ManagedApp, error) {
	var result api.ManagedApp
	err := remote.call([]string{"apps:read"}, func(client *api.Client, login auth.Login) error {
		var err error
		result, err = client.GetApp(remote.ctx, login.APIOrigin, login.Organization, id, login.AccessToken)
		return err
	})
	if err == nil && (result.Status == "deleted" || result.Status == "suspended") {
		return result, conflict()
	}
	return result, err
}

type keyState struct {
	detail     api.AppConnectionDetail
	credential *api.InferenceKey
}

func (remote *gateway) key(appID, id string) (keyState, error) {
	var result keyState
	err := remote.call([]string{"app_connections:read"}, func(client *api.Client, login auth.Login) error {
		detail, err := client.GetAppConnection(remote.ctx, login.APIOrigin, login.Organization, appID, id, login.AccessToken)
		if err != nil {
			return err
		}
		result.detail = detail
		keys, err := client.ListConnectionKeys(remote.ctx, login.APIOrigin, login.Organization, appID, id, login.AccessToken)
		if err != nil {
			return err
		}
		result.credential = keys.Connection.ActiveKey
		return nil
	})
	return result, err
}

func (remote *gateway) router(appID, id string) (api.RoutingKey, error) {
	var result api.RoutingKey
	err := remote.call([]string{"routing_keys:read"}, func(client *api.Client, login auth.Login) error {
		var err error
		result, err = client.GetRoutingKey(remote.ctx, login.APIOrigin, login.Organization, appID, id, login.AccessToken)
		return err
	})
	return result, err
}

func appFields(app api.ManagedApp) Fields {
	result := fields(app)
	redirects := []string{}
	for _, redirect := range app.RedirectURIs {
		if redirect.Status == "active" {
			redirects = append(redirects, redirect.URI)
		}
	}
	slices.Sort(redirects)
	result.set("redirect_uris", redirects)
	return result
}
func keyFields(key keyState) Fields {
	result := fields(key.detail.Connection)
	if key.credential != nil {
		result.set("key_alias", key.credential.Name)
		result.set("allowed_models", key.credential.AllowedModels)
	}
	return result
}
func capFields(key keyState) Fields {
	if key.detail.Cap == nil || key.detail.Cap.Status != "active" {
		return nil
	}
	return fields(key.detail.Cap).pick("amount", "currency", "period")
}
func routerFields(router api.RoutingKey) Fields {
	members := make([]Fields, 0, len(router.Members))
	for _, member := range router.Members {
		members = append(members, fields(map[string]string{"connection_id": member.ConnectionID, "selector_alias": member.Alias}))
	}
	sort.Slice(members, func(i, j int) bool { return members[i].text("selector_alias") < members[j].text("selector_alias") })
	return fields(map[string]any{"key_alias": router.Name, "members": members, "default_selector_alias": router.DefaultSelector, "status": router.Status})
}

func requireModels(models, allowed []string, pointer string) error {
	if len(models) == 0 {
		return manifestError("model_policy_required", "Define the app's allowedModels before creating or updating keys.", pointer)
	}
	if slices.Contains(allowed, "*") {
		return nil
	}
	for _, model := range models {
		if !slices.Contains(allowed, model) {
			return manifestError("model_not_allowed", "A key's model permissions must be a subset of the app's model permissions.", pointer)
		}
	}
	return nil
}

func manifestError(code, message, pointer string) error {
	failure := problem.New(code, message, 2)
	failure.Pointer = &pointer
	return failure
}
