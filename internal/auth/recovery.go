package auth

import (
	"context"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

// The caller keeps the credential lock until its API operation finishes.
func (service *Service) loginForOperation(ctx context.Context, target LoginTarget, scopes []string) (Login, func(), error) {
	unlock, err := service.Lock(ctx)
	if err != nil {
		return Login{}, nil, err
	}
	login, err := service.loadLogin(target.APIOrigin)
	if err != nil {
		unlock()
		return Login{}, nil, err
	}
	if err := requireOrganization(login, target.Organization); err != nil {
		unlock()
		return Login{}, nil, err
	}
	if err := requireScopes(login, scopes); err != nil {
		unlock()
		return Login{}, nil, err
	}
	current, err := service.renewLogin(ctx, login)
	if err == nil {
		if err := requireScopes(current, scopes); err != nil {
			unlock()
			return Login{}, nil, err
		}
		return current, unlock, nil
	}

	// Browser login also locks the credential store, so release it first.
	unlock()
	if service.RequestLogin == nil || ctx.Err() != nil || !canRecoverRenewal(err) {
		return Login{}, nil, err
	}
	err = service.RequestLogin(ctx, LoginOptions{
		APIOrigin: login.APIOrigin, DashboardOrigin: login.DashboardOrigin, Scope: login.Scope,
		ExpectedOrganization: login.Organization, ExpectedUser: target.UserID,
	})
	if err != nil {
		return Login{}, nil, err
	}

	// Read the saved replacement once. Never restart the command or replay a write.
	unlock, err = service.Lock(ctx)
	if err != nil {
		return Login{}, nil, err
	}
	current, err = service.loadLogin(login.APIOrigin)
	if err != nil {
		unlock()
		return Login{}, nil, err
	}
	if err := requireOrganization(current, login.Organization); err != nil {
		unlock()
		return Login{}, nil, err
	}
	current, err = service.renewLogin(ctx, current)
	if err != nil {
		unlock()
		return Login{}, nil, err
	}
	if err := requireScopes(current, scopes); err != nil {
		unlock()
		return Login{}, nil, err
	}
	if target.UserID != "" {
		user, err := service.loginUser(ctx, current)
		if err != nil {
			unlock()
			return Login{}, nil, err
		}
		if user != target.UserID {
			unlock()
			return Login{}, nil, loginTargetChanged()
		}
	}
	return current, unlock, nil
}

func canRecoverRenewal(err error) bool {
	switch problem.Safe(err).Code {
	case "login_required", "invalid_refresh_response", "credential_save_failed":
		return true
	default:
		return false
	}
}

func requireOrganization(login Login, organization string) error {
	if organization != "" && login.Organization != organization {
		return loginTargetChanged()
	}
	return nil
}

func requireScopes(login Login, scopes []string) error {
	for _, scope := range scopes {
		if !hasScope(login.Scope, scope) {
			return problem.New("authorization_required", "This operation needs "+strings.Join(scopes, " ")+". Log in again with those --scope values.", 3)
		}
	}
	return nil
}

func (service *Service) loginUser(ctx context.Context, login Login) (string, error) {
	me, err := service.API.GetMe(ctx, login.APIOrigin, login.Organization, login.AccessToken)
	return me.User.ID, err
}

func loginTargetChanged() error {
	return problem.New("target_mismatch", "Sign in with the same organization and user to continue this operation.", 4)
}
