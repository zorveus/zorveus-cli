package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

type Service struct {
	API   *api.Client
	Store Store
	Lock  func(context.Context) (func(), error)
	Now   func() time.Time
}

type LoginSummary struct {
	APIOrigin                string    `json:"api_origin"`
	Organization             string    `json:"organization"`
	Scope                    string    `json:"scope"`
	ExpiresAt                time.Time `json:"expires_at"`
	Renewal                  bool      `json:"renewal_allowed"`
	PreviousSessionMayRemain bool      `json:"previous_session_may_remain,omitempty"`
}

type AppsResult struct {
	Organization string    `json:"organization"`
	Apps         []api.App `json:"apps"`
	Limit        int       `json:"limit"`
	MayHaveMore  bool      `json:"may_have_more"`
}

type LogoutResult struct {
	LoggedOut bool `json:"logged_out"`
}

type WhoamiResult struct {
	APIOrigin    string        `json:"api_origin"`
	Organization string        `json:"organization"`
	Scope        string        `json:"scope"`
	User         api.MeUser    `json:"user"`
	Session      api.MeSession `json:"session"`
}

func (service *Service) UseLogin(ctx context.Context, expectedOrigin string, scopes []string, run func(Login) error) error {
	unlock, err := service.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	login, err := service.loadLogin(expectedOrigin)
	if err != nil {
		return err
	}
	current, err := service.renewLogin(ctx, login)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if !hasScope(current.Scope, scope) {
			return problem.New("authorization_required", "This operation needs "+strings.Join(scopes, " ")+". Log in again with those --scope values.", 3)
		}
	}
	return run(current)
}

func (service *Service) Whoami(ctx context.Context, expectedOrigin string) (WhoamiResult, error) {
	unlock, err := service.Lock(ctx)
	if err != nil {
		return WhoamiResult{}, err
	}
	defer unlock()
	login, err := service.loadLogin(expectedOrigin)
	if err != nil {
		return WhoamiResult{}, err
	}
	currentLogin, err := service.renewLogin(ctx, login)
	if err != nil {
		return WhoamiResult{}, err
	}
	me, err := service.API.GetMe(ctx, currentLogin.APIOrigin, currentLogin.Organization, currentLogin.AccessToken)
	if err != nil {
		return WhoamiResult{}, err
	}
	return WhoamiResult{
		APIOrigin: currentLogin.APIOrigin, Organization: currentLogin.Organization, Scope: currentLogin.Scope,
		User: me.User, Session: me.Session,
	}, nil
}

func (service *Service) Apps(ctx context.Context, expectedOrigin string, limit int) (AppsResult, error) {
	unlock, err := service.Lock(ctx)
	if err != nil {
		return AppsResult{}, err
	}
	defer unlock()
	login, err := service.loadLogin(expectedOrigin)
	if err != nil {
		return AppsResult{}, err
	}
	if !hasScope(login.Scope, "apps:read") {
		return AppsResult{}, problem.New("authorization_required", "This command needs apps:read. Run zorveus login --scope apps:read.", 3)
	}
	currentLogin, err := service.renewLogin(ctx, login)
	if err != nil {
		return AppsResult{}, err
	}
	result, err := service.API.ListApps(ctx, currentLogin.APIOrigin, currentLogin.Organization, currentLogin.AccessToken, limit)
	if err != nil {
		return AppsResult{}, err
	}
	return AppsResult{
		Organization: currentLogin.Organization,
		Apps:         result.Apps,
		Limit:        limit,
		MayHaveMore:  len(result.Apps) == limit,
	}, nil
}

func (service *Service) renewLogin(ctx context.Context, login Login) (Login, error) {
	if login.RefreshBlocked {
		return Login{}, problem.New("login_required", "The previous refresh did not complete safely. Run zorveus login. You can revoke the old session from the dashboard.", 3)
	}
	if service.Now().Add(5 * time.Second).Before(login.ExpiresAt) {
		return login, nil
	}
	if login.RefreshToken == "" {
		return Login{}, problem.New("login_required", "The access token expired and renewal was not approved. Run zorveus login.", 3)
	}
	login.RefreshBlocked = true
	if err := service.Store.Save(login); err != nil {
		return Login{}, err
	}
	requestedAt := service.Now()
	tokens, err := service.API.Refresh(ctx, login.APIOrigin, login.RefreshToken)
	if err != nil {
		return Login{}, problem.New("login_required", "Could not complete token renewal. Run zorveus login. The previous refresh token will not be retried.", 3)
	}
	if tokens.OrgID != login.Organization || tokens.RefreshToken == "" || !scopesAreSubset(tokens.Scope, login.Scope) {
		return Login{}, problem.New("invalid_refresh_response", "The renewal response changed the authorized target or permissions. Run zorveus login and revoke the old session from the dashboard.", 3)
	}
	replacement := loginFromTokens(login.APIOrigin, tokens, requestedAt)
	if err := service.Store.Save(replacement); err != nil {
		return Login{}, problem.New("credential_save_failed", "The renewed credentials could not be saved. Run zorveus login and revoke the old session from the dashboard.", 3)
	}
	return replacement, nil
}

func (service *Service) Logout(ctx context.Context, expectedOrigin string) (LogoutResult, error) {
	unlock, err := service.Lock(ctx)
	if err != nil {
		return LogoutResult{}, err
	}
	defer unlock()
	login, err := service.loadLogin(expectedOrigin)
	if errors.Is(err, ErrNoLogin) {
		return LogoutResult{LoggedOut: true}, nil
	}
	if err != nil {
		return LogoutResult{}, err
	}
	if login.RefreshBlocked {
		return LogoutResult{}, problem.New("logout_incomplete", "The previous refresh had an uncertain outcome, so the saved tokens cannot confirm remote revocation. Revoke the session from the dashboard. The local record was retained.", 1)
	}
	if err := service.revokeLogin(ctx, login); err != nil {
		return LogoutResult{}, problem.New("logout_incomplete", "Could not revoke the login. Saved credentials were retained so you can retry logout. You can also revoke the session from the dashboard.", 1)
	}
	if err := service.Store.Delete(); err != nil {
		return LogoutResult{}, problem.New("logout_incomplete", "The remote login was revoked, but the saved credentials could not be removed. Retry logout after unlocking your credential store.", 1)
	}
	return LogoutResult{LoggedOut: true}, nil
}

func (service *Service) loadLogin(expectedOrigin string) (Login, error) {
	login, err := service.Store.Load()
	if err != nil {
		return Login{}, err
	}
	origin, err := api.CanonicalOrigin(login.APIOrigin, true)
	if err != nil || origin != login.APIOrigin || login.ClientID != api.ClientID || login.Organization == "" || login.AccessToken == "" || login.ExpiresAt.IsZero() {
		return Login{}, problem.New("invalid_saved_login", "The saved login is incomplete. Remove the current-login entry for com.zorveus.cli from your OS credential store, then log in again.", 3)
	}
	if expectedOrigin != "" && expectedOrigin != login.APIOrigin {
		return Login{}, problem.New("target_mismatch", "The API origin differs from the saved login. Log in to that origin before using it.", 4)
	}
	return login, nil
}

func (service *Service) revokeLogin(ctx context.Context, login Login) error {
	token := login.RefreshToken
	if token == "" {
		token = login.AccessToken
	}
	return service.API.Revoke(ctx, login.APIOrigin, token)
}

func loginFromTokens(origin string, tokens api.Tokens, requestedAt time.Time) Login {
	return Login{
		APIOrigin:    origin,
		ClientID:     api.ClientID,
		Organization: tokens.OrgID,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    requestedAt.Add(time.Duration(tokens.ExpiresIn) * time.Second),
		Scope:        tokens.Scope,
	}
}

func hasScope(granted, required string) bool {
	for _, scope := range strings.Fields(granted) {
		if scope == required {
			return true
		}
	}
	return false
}

func scopesAreSubset(granted, requested string) bool {
	for _, scope := range strings.Fields(granted) {
		if !hasScope(requested, scope) {
			return false
		}
	}
	return true
}
