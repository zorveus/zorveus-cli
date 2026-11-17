package auth

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func reference(login Login) LoginReference {
	return LoginReference{APIOrigin: login.APIOrigin, Organization: login.Organization}
}

func OrganizationLabel(name, id string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return id
	}
	return name + " (" + id + ")"
}

// Callers already hold the credential lock while reading /me.
func (service *Service) RememberOrganizationName(login Login, organizations []api.Workspace) string {
	for _, organization := range organizations {
		name := strings.TrimSpace(organization.Name)
		if organization.ID != login.Organization || name == "" {
			continue
		}
		if name != login.OrganizationName {
			login.OrganizationName = name
			// A display-name cache failure must not fail an authenticated operation.
			_ = service.saveLogin(login)
		}
		return name
	}
	return login.OrganizationName
}

func (saved SavedLogins) find(target LoginReference) (Login, bool) {
	for _, login := range saved.Logins {
		if reference(login) == target {
			return login, true
		}
	}
	return Login{}, false
}

func (saved *SavedLogins) put(login Login) {
	saved.Version = 1
	for index, previous := range saved.Logins {
		if reference(previous) == reference(login) {
			saved.Logins[index] = login
			return
		}
	}
	saved.Logins = append(saved.Logins, login)
}

// Credential writes happen while the caller holds the shared credential lock.
func (service *Service) saveLogin(login Login) error {
	saved, err := service.Store.Read()
	if err != nil && !errors.Is(err, ErrNoLogin) {
		return err
	}
	saved.put(login)
	return service.Store.Write(saved)
}

func (service *Service) loadLogin(ctx context.Context, target LoginTarget) (Login, error) {
	if target.Organization != "" && service.Organization != "" && target.Organization != service.Organization {
		return Login{}, problem.New("organization_mismatch", "--org differs from this project's organization. Use its organization or select a different project.", 4)
	}
	organization := target.Organization
	if organization == "" {
		organization = service.Organization
	}
	saved, err := service.Store.Read()
	if err != nil {
		return Login{}, err
	}
	choices := []LoginReference{}
	for _, login := range saved.Logins {
		if target.APIOrigin != "" && login.APIOrigin != target.APIOrigin {
			continue
		}
		if organization != "" && login.Organization != organization {
			continue
		}
		choices = append(choices, reference(login))
	}
	if len(choices) == 0 {
		if organization != "" {
			return Login{}, problem.New("login_required", "No login is saved for organization "+organization+" at this API origin. Run zorveus login --org "+organization+" with the appropriate API settings.", 3)
		}
		if len(saved.Logins) != 0 && target.APIOrigin != "" {
			return Login{}, problem.New("login_required", "No login is saved for this API origin. Log in to that origin before using it.", 3)
		}
		return Login{}, ErrNoLogin
	}
	selected := choices[0]
	if len(choices) > 1 {
		if slices.Contains(choices, saved.Default) {
			selected = saved.Default
		} else {
			if service.ChooseOrganization == nil {
				return Login{}, problem.New("organization_required", "Several organizations are saved and none is the default for this API origin. Pass --org or run zorveus login to set a default.", 3)
			}
			selected, err = service.ChooseOrganization(ctx, choices)
			if err != nil {
				return Login{}, err
			}
			if !slices.Contains(choices, selected) {
				return Login{}, problem.New("organization_required", "Choose one of the saved organizations.", 3)
			}
		}
	}
	login, _ := saved.find(selected)
	if login.AccessToken == "" {
		return Login{}, problem.New("login_required", "The saved credentials for organization "+login.Organization+" are missing. Run zorveus login --org "+login.Organization+" with the appropriate API settings.", 3)
	}
	origin, err := api.CanonicalOrigin(login.APIOrigin, true)
	if err != nil || origin != login.APIOrigin || login.ClientID != api.ClientID || login.Organization == "" || login.AccessToken == "" || login.ExpiresAt.IsZero() {
		return Login{}, invalidSavedLogins()
	}
	return login, nil
}

func (service *Service) SetDefault(ctx context.Context, target LoginReference) error {
	unlock, err := service.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	saved, err := service.Store.Read()
	if err != nil {
		return err
	}
	if _, exists := saved.find(target); !exists {
		return ErrNoLogin
	}
	saved.Default = target
	return service.Store.Write(saved)
}

func (service *Service) deleteLogin(login Login) error {
	saved, err := service.Store.Read()
	if err != nil {
		return err
	}
	saved.Logins = slices.DeleteFunc(saved.Logins, func(candidate Login) bool {
		return reference(candidate) == reference(login)
	})
	if saved.Default == reference(login) {
		saved.Default = LoginReference{}
	}
	return service.Store.Write(saved)
}
