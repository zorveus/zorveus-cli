package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

type ServiceKey struct {
	TokenPrefix  string     `json:"token_prefix"`
	ExpiresAt    *time.Time `json:"expires_at"`
	ID           string     `json:"org_service_api_key_id"`
	Organization string     `json:"org_id"`
	Name         *string    `json:"key_alias"`
	Scopes       []string   `json:"scopes"`
	Status       string     `json:"status"`
}

type ProviderCredential struct {
	ID           string          `json:"provider_credential_id"`
	Organization string          `json:"org_id"`
	Provider     string          `json:"provider"`
	Name         string          `json:"credential_name"`
	Status       string          `json:"status"`
	RoutingMode  string          `json:"routing_mode"`
	Models       []string        `json:"default_model_policy"`
	VersionID    *string         `json:"active_secret_version_id"`
	Config       *ProviderConfig `json:"provider_config"`
}

type ProviderConfig struct {
	APIBase    string `json:"api_base,omitempty"`
	APIVersion string `json:"api_version,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
	Project    string `json:"project,omitempty"`
	Location   string `json:"location,omitempty"`
}

type ServiceKeyCredential struct {
	Key    ServiceKey `json:"metadata"`
	Secret string     `json:"-"`
}

func (client *Client) RotateServiceKey(ctx context.Context, origin, organization, id, token string) (ServiceKeyCredential, error) {
	if err := requireResourceIDs(organization, id); err != nil {
		return ServiceKeyCredential{}, err
	}
	var response struct {
		ServiceKey
		Secret string `json:"service_key"`
	}
	path := "/orgs/" + organization + "/service-keys/" + id + "/rotate"
	if err := client.WriteJSON(ctx, origin, http.MethodPost, path, token, nil, &response); err != nil {
		return ServiceKeyCredential{}, err
	}
	if response.ID != id || response.Organization != organization {
		return ServiceKeyCredential{}, unexpectedResource()
	}
	if response.Secret == "" || response.TokenPrefix == "" {
		return ServiceKeyCredential{}, problem.New("invalid_response", "The API did not return the replacement service-key credential.", 1)
	}
	return ServiceKeyCredential{Key: response.ServiceKey, Secret: response.Secret}, nil
}

func (client *Client) ListServiceKeys(ctx context.Context, origin, organization, token string) ([]ServiceKey, error) {
	if err := requireResourceIDs(organization); err != nil {
		return nil, err
	}
	var response struct {
		Keys []ServiceKey `json:"service_keys"`
	}
	if err := client.readResource(ctx, origin, "/orgs/"+organization+"/service-keys", token, &response); err != nil {
		return nil, err
	}
	for _, key := range response.Keys {
		if !resourceIDPattern.MatchString(key.ID) || key.Organization != organization {
			return nil, unexpectedResource()
		}
	}
	return response.Keys, nil
}

func (client *Client) RevokeServiceKey(ctx context.Context, origin, organization, id, token string) (ServiceKey, error) {
	if err := requireResourceIDs(organization, id); err != nil {
		return ServiceKey{}, err
	}
	var response struct {
		Key     ServiceKey `json:"service_key"`
		Revoked bool       `json:"revoked"`
	}
	path := "/orgs/" + organization + "/service-keys/" + id + "/revoke"
	if err := client.WriteJSON(ctx, origin, http.MethodPost, path, token, nil, &response); err != nil {
		return ServiceKey{}, err
	}
	if response.Key.ID != id || response.Key.Organization != organization {
		return ServiceKey{}, unexpectedResource()
	}
	if response.Key.Status != "revoked" {
		return ServiceKey{}, problem.New("invalid_response", "The API did not confirm service-key revocation.", 1)
	}
	return response.Key, nil
}

func (client *Client) ListProviderCredentials(ctx context.Context, origin, organization, token string) ([]ProviderCredential, error) {
	if err := requireResourceIDs(organization); err != nil {
		return nil, err
	}
	const pageSize = 1000
	query := url.Values{"org_id": {organization}, "limit": {strconv.Itoa(pageSize)}}
	credentials := []ProviderCredential{}
	seen := map[string]bool{}
	for offset := 0; ; offset += pageSize {
		query.Set("offset", strconv.Itoa(offset))
		var page struct {
			Credentials []ProviderCredential `json:"provider_credentials"`
		}
		if err := client.readResource(ctx, origin, "/provider-credentials?"+query.Encode(), token, &page); err != nil {
			return nil, err
		}
		if len(page.Credentials) > pageSize {
			return nil, invalidPage()
		}
		for _, credential := range page.Credentials {
			if !resourceIDPattern.MatchString(credential.ID) || credential.Organization != organization {
				return nil, unexpectedResource()
			}
			if seen[credential.ID] {
				return nil, changedCollection()
			}
			seen[credential.ID] = true
			credentials = append(credentials, credential)
		}
		// A full page requires another request, even when the total is exactly 1,000.
		if len(page.Credentials) < pageSize {
			return credentials, nil
		}
	}
}

func invalidPage() error {
	return problem.New("invalid_response", "The API returned more records than the requested page size.", 1)
}
func changedCollection() error {
	return problem.New("collection_changed", "A resource appeared twice while paging. Retry the read after concurrent changes settle.", 4)
}
