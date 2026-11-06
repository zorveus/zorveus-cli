package api

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

var resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type ManagedApp struct {
	App
	ClientType                  string        `json:"client_type"`
	AllowedScopes               []string      `json:"allowed_scopes"`
	CreditUnitsPerUSD           *string       `json:"credit_units_per_usd"`
	DefaultCapAmount            *string       `json:"default_cap_amount"`
	MinimumCapAmount            *string       `json:"minimum_cap_amount"`
	MaximumCapAmount            *string       `json:"maximum_cap_amount"`
	AllowUnlimitedCap           bool          `json:"allow_unlimited_cap"`
	DefaultCapCurrency          *string       `json:"default_cap_currency"`
	DefaultCapPeriod            *string       `json:"default_cap_period"`
	ProductUserChargeMultiplier string        `json:"product_user_charge_multiplier"`
	RedirectURIs                []RedirectURI `json:"redirect_uris"`
}

type RedirectURI struct {
	ID     string `json:"redirect_uri_id"`
	URI    string `json:"redirect_uri"`
	Status string `json:"status"`
}

type InferenceKey struct {
	ID            string     `json:"api_key_id"`
	ConnectionID  string     `json:"app_connection_id"`
	TokenPrefix   string     `json:"token_prefix"`
	Status        string     `json:"status"`
	Name          *string    `json:"key_alias"`
	Scopes        []string   `json:"scopes"`
	AllowedModels []string   `json:"allowed_models"`
	ExpiresAt     *time.Time `json:"expires_at"`
}

type AppConnection struct {
	ID                                   string        `json:"app_connection_id"`
	AppID                                string        `json:"app_id"`
	Organization                         string        `json:"selected_org_id"`
	Type                                 string        `json:"connection_type"`
	Status                               string        `json:"status"`
	CreditMode                           string        `json:"credit_mode"`
	OverrunPolicy                        string        `json:"overrun_policy"`
	ApprovedScopes                       []string      `json:"approved_scopes"`
	AllowedModels                        []string      `json:"allowed_models"`
	ActiveKey                            *InferenceKey `json:"active_key"`
	ProductUserChargeMultiplier          *string       `json:"product_user_charge_multiplier"`
	EffectiveProductUserChargeMultiplier string        `json:"effective_product_user_charge_multiplier"`
}

type ConnectionCap struct {
	ID           string     `json:"cap_rule_id"`
	Organization string     `json:"org_id"`
	TargetType   string     `json:"target_type"`
	TargetID     string     `json:"target_id"`
	Amount       string     `json:"amount"`
	Currency     string     `json:"currency"`
	Period       string     `json:"period"`
	Status       string     `json:"status"`
	ResetAt      *time.Time `json:"reset_at"`
}

type AppConnectionDetail struct {
	Connection AppConnection  `json:"app_connection"`
	Cap        *ConnectionCap `json:"cap_rule"`
}

type ConnectionKeyList struct {
	Connection AppConnection  `json:"app_connection"`
	Keys       []InferenceKey `json:"api_keys"`
}

type RoutingMember struct {
	ConnectionID string `json:"app_connection_id"`
	KeyID        string `json:"selected_api_key_id"`
	Alias        string `json:"selector_alias"`
	IsDefault    bool   `json:"is_default"`
	Status       string `json:"status"`
}

type RoutingKey struct {
	KeyID           string          `json:"api_key_id"`
	AppID           string          `json:"app_id"`
	Organization    string          `json:"org_id"`
	Name            *string         `json:"key_alias"`
	Status          string          `json:"status"`
	DefaultSelector *string         `json:"default_selector_alias"`
	Members         []RoutingMember `json:"members"`
}

func (client *Client) GetApp(ctx context.Context, origin, organization, appID, token string) (ManagedApp, error) {
	if err := requireResourceIDs(organization, appID); err != nil {
		return ManagedApp{}, err
	}
	var result struct {
		App ManagedApp `json:"app"`
	}
	if err := client.readResource(ctx, origin, "/apps/"+appID, token, &result); err != nil {
		return ManagedApp{}, err
	}
	if result.App.ID != appID || result.App.Organization != organization {
		return ManagedApp{}, unexpectedResource()
	}
	return result.App, nil
}

func (client *Client) ListAppConnections(ctx context.Context, origin, organization, appID, token string, pageSize int) ([]AppConnection, error) {
	if err := requireResourceIDs(organization, appID); err != nil {
		return nil, err
	}
	if pageSize < 1 || pageSize > 1000 {
		return nil, problem.New("invalid_limit", "Use a connection page size between 1 and 1000.", 2)
	}
	query := url.Values{
		"org_id": {organization}, "app_id": {appID}, "limit": {strconv.Itoa(pageSize)},
		"connection_type": {"org_programmatic"}, "sort_by": {"created_at"}, "sort_order": {"asc"},
	}
	connections := []AppConnection{}
	seen := map[string]bool{}
	for offset := 0; ; offset += pageSize {
		query.Set("offset", strconv.Itoa(offset))
		var page struct {
			Connections []AppConnection `json:"app_connections"`
		}
		if err := client.readResource(ctx, origin, "/app-connections?"+query.Encode(), token, &page); err != nil {
			return nil, err
		}
		if len(page.Connections) > pageSize {
			return nil, invalidPage()
		}
		for _, connection := range page.Connections {
			if err := connection.checkTarget(organization, appID); err != nil {
				return nil, err
			}
			if seen[connection.ID] {
				return nil, changedCollection()
			}
			seen[connection.ID] = true
			connections = append(connections, connection)
		}
		if len(page.Connections) < pageSize {
			return connections, nil
		}
	}
}

func (client *Client) GetAppConnection(ctx context.Context, origin, organization, appID, connectionID, token string) (AppConnectionDetail, error) {
	if err := requireResourceIDs(organization, appID, connectionID); err != nil {
		return AppConnectionDetail{}, err
	}
	query := url.Values{"org_id": {organization}, "app_id": {appID}}
	var result AppConnectionDetail
	if err := client.readResource(ctx, origin, "/app-connections/"+connectionID+"?"+query.Encode(), token, &result); err != nil {
		return AppConnectionDetail{}, err
	}
	if err := result.Connection.checkTarget(organization, appID); err != nil {
		return AppConnectionDetail{}, err
	}
	if result.Connection.ID != connectionID {
		return AppConnectionDetail{}, unexpectedResource()
	}
	if result.Cap != nil && (result.Cap.Organization != organization || result.Cap.TargetType != "app_connection" || result.Cap.TargetID != connectionID) {
		return AppConnectionDetail{}, unexpectedResource()
	}
	return result, nil
}

func (client *Client) ListConnectionKeys(ctx context.Context, origin, organization, appID, connectionID, token string) (ConnectionKeyList, error) {
	if err := requireResourceIDs(organization, appID, connectionID); err != nil {
		return ConnectionKeyList{}, err
	}
	query := url.Values{"org_id": {organization}, "app_id": {appID}}
	var result ConnectionKeyList
	if err := client.readResource(ctx, origin, "/app-connections/"+connectionID+"/keys?"+query.Encode(), token, &result); err != nil {
		return ConnectionKeyList{}, err
	}
	if err := result.Connection.checkTarget(organization, appID); err != nil {
		return ConnectionKeyList{}, err
	}
	if result.Connection.ID != connectionID {
		return ConnectionKeyList{}, unexpectedResource()
	}
	for _, key := range result.Keys {
		if key.ID == "" || key.ConnectionID != connectionID {
			return ConnectionKeyList{}, unexpectedResource()
		}
	}
	return result, nil
}

func (client *Client) ListRoutingKeys(ctx context.Context, origin, organization, appID, token string) ([]RoutingKey, error) {
	if err := requireResourceIDs(organization, appID); err != nil {
		return nil, err
	}
	query := url.Values{"app_id": {appID}}
	var result struct {
		Keys []RoutingKey `json:"items"`
	}
	path := "/orgs/" + organization + "/routing-keys?" + query.Encode()
	if err := client.readResource(ctx, origin, path, token, &result); err != nil {
		return nil, err
	}
	for _, key := range result.Keys {
		if err := key.checkTarget(organization, appID); err != nil {
			return nil, err
		}
	}
	return result.Keys, nil
}

func (client *Client) GetRoutingKey(ctx context.Context, origin, organization, appID, keyID, token string) (RoutingKey, error) {
	if err := requireResourceIDs(organization, appID, keyID); err != nil {
		return RoutingKey{}, err
	}
	var key RoutingKey
	path := "/orgs/" + organization + "/apps/" + appID + "/routing-keys/" + keyID
	if err := client.readResource(ctx, origin, path, token, &key); err != nil {
		return RoutingKey{}, err
	}
	if err := key.checkTarget(organization, appID); err != nil {
		return RoutingKey{}, err
	}
	if key.KeyID != keyID {
		return RoutingKey{}, unexpectedResource()
	}
	return key, nil
}

func (client *Client) readResource(ctx context.Context, origin, path, token string, destination any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+path, nil)
	if err != nil {
		return problem.New("invalid_request", "Could not prepare the management request.", 1)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	return client.send(request, destination)
}

func (connection AppConnection) checkTarget(organization, appID string) error {
	if connection.ID == "" || connection.Organization != organization || connection.AppID != appID || connection.Type != "org_programmatic" {
		return unexpectedResource()
	}
	if connection.ActiveKey != nil && (connection.ActiveKey.ID == "" || connection.ActiveKey.ConnectionID != connection.ID) {
		return unexpectedResource()
	}
	return nil
}

func (key RoutingKey) checkTarget(organization, appID string) error {
	if key.KeyID == "" || key.Organization != organization || key.AppID != appID {
		return unexpectedResource()
	}
	for _, member := range key.Members {
		if member.ConnectionID == "" || member.KeyID == "" {
			return unexpectedResource()
		}
	}
	return nil
}

func requireResourceIDs(identifiers ...string) error {
	for _, identifier := range identifiers {
		if !resourceIDPattern.MatchString(identifier) {
			return problem.New("invalid_resource_id", "Provide a valid organization, app, or resource ID.", 2)
		}
	}
	return nil
}

func unexpectedResource() error {
	return problem.New("resource_mismatch", "The API returned a resource outside the requested target.", 4)
}
