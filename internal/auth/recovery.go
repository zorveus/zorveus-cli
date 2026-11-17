package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

// The caller keeps the credential lock until its API operation finishes.
func (service *Service) loginForOperation(ctx context.Context, target LoginTarget, scopes []string) (Login, func(), error) {
	unlock, err := service.Lock(ctx)
	if err != nil {
		return Login{}, nil, err
	}
	login, err := service.loadLogin(ctx, target)
	if err != nil {
		if service.RequestLogin == nil || ctx.Err() != nil || !missingLogin(err) {
			unlock()
			return Login{}, nil, err
		}
		organization := target.Organization
		if organization == "" {
			organization = service.Organization
		}
		options := LoginOptions{
			APIOrigin: target.APIOrigin, ExpectedOrganization: organization,
		}
		saved, err := service.Store.Read()
		if err != nil && !errors.Is(err, ErrNoLogin) {
			unlock()
			return Login{}, nil, err
		}
		if options.APIOrigin == "" {
			// Reuse the configured environment when only this organization's login is missing.
			environmentLogin, found := saved.find(saved.Default)
			if !found && len(saved.Logins) == 1 {
				environmentLogin, found = saved.Logins[0], true
			}
			if found {
				options.APIOrigin = environmentLogin.APIOrigin
				options.DashboardOrigin = environmentLogin.DashboardOrigin
			}
		}
		options.SignInMessage = "Sign in to continue."
		if organization != "" {
			cached, _ := saved.find(LoginReference{APIOrigin: options.APIOrigin, Organization: organization})
			options.SignInMessage = "Sign in to organization " + OrganizationLabel(cached.OrganizationName, organization) + " to continue."
		}
		unlock()
		return service.signInForOperation(ctx, target, scopes, options)
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
	return service.signInForOperation(ctx, target, scopes, LoginOptions{
		APIOrigin: login.APIOrigin, DashboardOrigin: login.DashboardOrigin, Scope: login.Scope,
		SignInMessage: "Your session for " + OrganizationLabel(login.OrganizationName, login.Organization) + " could not be renewed. Sign in again to continue.",
	})
}

func missingLogin(err error) bool {
	return errors.Is(err, ErrNoLogin) || problem.Safe(err).Code == "login_required"
}

func (service *Service) signInForOperation(ctx context.Context, target LoginTarget, scopes []string, options LoginOptions) (Login, func(), error) {
	recovered, err := service.RequestLogin(ctx, options)
	if err != nil {
		return Login{}, nil, err
	}
	if options.APIOrigin != "" && recovered.APIOrigin != options.APIOrigin {
		return Login{}, nil, loginTargetChanged()
	}
	if target.Organization != "" && recovered.Organization != target.Organization {
		return Login{}, nil, loginTargetChanged()
	}
	if service.Organization != "" && recovered.Organization != service.Organization {
		return Login{}, nil, loginTargetChanged()
	}
	target.APIOrigin = recovered.APIOrigin
	target.Organization = recovered.Organization

	// Read the saved replacement once. Never restart the command or replay a write.
	unlock, err := service.Lock(ctx)
	if err != nil {
		return Login{}, nil, err
	}
	current, err := service.loadLogin(ctx, target)
	if err != nil {
		unlock()
		return Login{}, nil, err
	}
	if err := requireOrganization(current, target.Organization); err != nil {
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
	return problem.New("target_mismatch", "This operation belongs to another organization or user. Your saved logins were kept. Use the original organization and user to resume it, or run plan for a different project.", 4)
}
