package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func (client *Client) DeleteApp(ctx context.Context, origin, organization, id, token string) error {
	if err := requireResourceIDs(organization, id); err != nil {
		return err
	}
	var response struct {
		ID      string `json:"app_id"`
		Deleted bool   `json:"deleted"`
	}
	if err := client.WriteJSON(ctx, origin, http.MethodDelete, "/apps/"+id, token, nil, &response); err != nil {
		return err
	}
	if response.ID != id || !response.Deleted {
		return problem.New("invalid_response", "The API did not confirm deletion of the selected app.", 1)
	}
	return nil
}

func (client *Client) DeleteProviderCredential(ctx context.Context, origin, organization, id, token string) error {
	if err := requireResourceIDs(organization, id); err != nil {
		return err
	}
	var response struct {
		Credential ProviderCredential `json:"provider_credential"`
	}
	path := "/provider-credentials/" + id + "?" + url.Values{"org_id": {organization}}.Encode()
	if err := client.WriteJSON(ctx, origin, http.MethodDelete, path, token, nil, &response); err != nil {
		return err
	}
	if response.Credential.ID != id || response.Credential.Organization != organization || response.Credential.Status != "deleted" {
		return problem.New("invalid_response", "The API did not confirm deletion of the selected provider credential.", 1)
	}
	return nil
}
