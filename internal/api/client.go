package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

const ClientID = "zrv_client_zorveus_cli"

type Client struct {
	HTTP *http.Client
}

type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	OrgID        string `json:"org_id"`
}

type App struct {
	ID            string   `json:"app_id"`
	Name          string   `json:"app_name"`
	Organization  string   `json:"owner_org_id"`
	Status        string   `json:"status"`
	AllowedModels []string `json:"allowed_models"`
}

type AppList struct {
	Apps []App `json:"apps"`
}

func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func CanonicalOrigin(raw string, allowLocalHTTP bool) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.Opaque != "" {
		return "", problem.New("invalid_origin", "Provide a valid API or dashboard origin.", 2)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", problem.New("invalid_origin", "An origin must contain only a scheme, hostname, and optional port.", 2)
	}
	address := net.ParseIP(parsed.Hostname())
	isLoopback := strings.EqualFold(parsed.Hostname(), "localhost") || (address != nil && address.IsLoopback())
	if parsed.Scheme != "https" && !(allowLocalHTTP && parsed.Scheme == "http" && isLoopback) {
		return "", problem.New("insecure_origin", "Use HTTPS, or explicitly allow HTTP on localhost or a loopback IP address with --allow-insecure-loopback.", 2)
	}
	port := parsed.Port()
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		parsed.Host = parsed.Hostname()
		if strings.Contains(parsed.Host, ":") {
			parsed.Host = "[" + parsed.Host + "]"
		}
	}
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed.String(), nil
}

func (client *Client) ExchangeCode(ctx context.Context, origin, code, callback, verifier string) (Tokens, error) {
	return client.requestTokens(ctx, origin, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {code},
		"redirect_uri":  {callback},
		"code_verifier": {verifier},
	})
}

func (client *Client) Refresh(ctx context.Context, origin, refreshToken string) (Tokens, error) {
	return client.requestTokens(ctx, origin, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {ClientID},
		"refresh_token": {refreshToken},
	})
}

func (client *Client) requestTokens(ctx context.Context, origin string, form url.Values) (Tokens, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/oauth/management/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, problem.New("invalid_request", "Could not prepare the token request.", 1)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tokens Tokens
	if err := client.send(request, &tokens); err != nil {
		return Tokens{}, err
	}
	if tokens.AccessToken == "" || tokens.OrgID == "" || tokens.ExpiresIn <= 0 || !strings.EqualFold(tokens.TokenType, "Bearer") || tokens.Scope == "" {
		return Tokens{}, problem.New("invalid_token_response", "The API returned an incomplete token response. Log in again.", 3)
	}
	return tokens, nil
}

func (client *Client) Revoke(ctx context.Context, origin, token string) error {
	form := url.Values{"client_id": {ClientID}, "token": {token}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/oauth/management/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		return problem.New("invalid_request", "Could not prepare the revocation request.", 1)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return client.send(request, nil)
}

func (client *Client) ListApps(ctx context.Context, origin, organization, accessToken string, limit int) (AppList, error) {
	query := url.Values{"owner_org_id": {organization}}
	query.Set("limit", strconv.Itoa(limit))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/apps?"+query.Encode(), nil)
	if err != nil {
		return AppList{}, problem.New("invalid_request", "Could not prepare the app-list request.", 1)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	result := AppList{Apps: []App{}}
	if err := client.send(request, &result); err != nil {
		return AppList{}, err
	}
	for _, app := range result.Apps {
		if app.Organization != organization {
			return AppList{}, problem.New("organization_mismatch", "The API returned an app outside the authorized organization.", 4)
		}
	}
	return result, nil
}

func (client *Client) send(request *http.Request, destination any) error {
	httpClient := *client.HTTP
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(request)
	if err != nil {
		return problem.New("request_failed", "The API request failed or timed out. Its outcome may be unknown.", 1)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response)
	}
	if destination == nil {
		// Complete ignored responses before reusing the connection for a read-back.
		if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, 4<<20)); err != nil {
			return problem.New("invalid_response", "The API response could not be completed.", 1)
		}
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(destination); err != nil {
		return problem.New("invalid_response", "The API returned an unreadable response.", 1)
	}
	return nil
}

func responseError(response *http.Response) error {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body)
	if body.Error == "invalid_grant" || body.Error == "invalid_token" || response.StatusCode == http.StatusUnauthorized {
		return problem.New("login_required", "Authorization expired or is no longer valid. Run zorveus login.", 3)
	}
	if body.Error == "insufficient_scope" || body.Error == "invalid_scope" {
		return problem.New("authorization_required", "The operation needs additional approved scopes. Run zorveus login with the required --scope values.", 3)
	}
	if response.StatusCode == http.StatusForbidden {
		return problem.New("access_denied", "The API denied access. Check your organization role and approved permissions.", 1)
	}
	if response.StatusCode == http.StatusNotFound {
		return problem.New("resource_not_found", "The requested resource was not found.", 1)
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return problem.New("redirect_refused", "The API returned a redirect. Check the configured API origin.", 1)
	}
	return problem.New("api_error", "The API rejected the request. Check the service and try again.", 1)
}
