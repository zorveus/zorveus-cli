package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func (client *Client) RenameRoutingKey(ctx context.Context, origin, organization, appID, keyID, name, token string) (RoutingKey, error) {
	if err := requireResourceIDs(organization, appID, keyID); err != nil {
		return RoutingKey{}, err
	}
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 255 {
		return RoutingKey{}, problem.New("invalid_name", "Provide a routing-key name between 1 and 255 characters.", 2)
	}
	payload := struct {
		KeyAlias string `json:"key_alias"`
	}{KeyAlias: name}
	path := "/orgs/" + organization + "/apps/" + appID + "/routing-keys/" + keyID
	var result RoutingKey
	if err := client.sendJSON(ctx, origin, http.MethodPatch, path, token, payload, &result); err != nil {
		return RoutingKey{}, err
	}
	if err := result.checkTarget(organization, appID); err != nil {
		return RoutingKey{}, err
	}
	if result.KeyID != keyID {
		return RoutingKey{}, unexpectedResource()
	}
	if result.Name == nil || *result.Name != name {
		return RoutingKey{}, problem.New("invalid_response", "The API did not confirm the updated routing-key name.", 1)
	}
	return result, nil
}

func (client *Client) sendJSON(ctx context.Context, origin, method, path, token string, payload, destination any) error {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return problem.New("invalid_request", "Could not encode the API request.", 1)
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, origin+path, bytes.NewReader(body))
	if err != nil {
		return problem.New("invalid_request", "Could not prepare the API request.", 1)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	return client.send(request, destination)
}

func (client *Client) WriteJSON(ctx context.Context, origin, method, path, token string, payload, destination any) error {
	return client.sendJSON(ctx, origin, method, path, token, payload, destination)
}
