package provision

import (
	"context"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

type ServiceKeysResult struct {
	Organization string           `json:"organization"`
	Keys         []api.ServiceKey `json:"service_keys"`
}

func (service *Service) ServiceKeys(ctx context.Context, origin string) (ServiceKeysResult, error) {
	remote := &gateway{ctx: ctx, service: service, target: Target{Origin: origin}}
	if err := remote.identify(); err != nil {
		return ServiceKeysResult{}, err
	}
	keys, err := remote.serviceKeys()
	if err != nil {
		return ServiceKeysResult{}, err
	}
	if keys == nil {
		keys = []api.ServiceKey{}
	}
	return ServiceKeysResult{Organization: remote.target.Org, Keys: keys}, nil
}

func (service *Service) RevokeServiceKey(ctx context.Context, origin, id string, accepted bool) (api.ServiceKey, error) {
	if !resourceID.MatchString(id) {
		return api.ServiceKey{}, problem.New("invalid_resource_id", "Provide a service-key ID with --id.", 2)
	}
	if !accepted {
		return api.ServiceKey{}, problem.New("acceptance_required", "Service keys belong to the organization and may be used by other apps. Pass --yes to confirm revocation.", 2)
	}
	remote := &gateway{ctx: ctx, service: service, target: Target{Origin: origin}}
	if err := remote.identify(); err != nil {
		return api.ServiceKey{}, err
	}
	var result api.ServiceKey
	err := remote.call([]string{"service_keys:read", "service_keys:revoke"}, func(client *api.Client, login auth.Login) error {
		keys, err := client.ListServiceKeys(ctx, login.APIOrigin, login.Organization, login.AccessToken)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if key.ID != id {
				continue
			}
			if key.Status == "revoked" {
				result = key
				return nil
			}
			result, err = client.RevokeServiceKey(ctx, login.APIOrigin, login.Organization, id, login.AccessToken)
			return err
		}
		return problem.New("resource_not_found", "The service key was not found in the approved organization.", 1)
	})
	return result, err
}
