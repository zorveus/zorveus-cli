package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func accountService(t *testing.T, logins ...Login) *Service {
	t.Helper()
	keyring.MockInit()
	store := CredentialStore{Directory: t.TempDir()}
	if err := store.Write(SavedLogins{Version: 1, Logins: logins}); err != nil {
		t.Fatal(err)
	}
	service := testService(&memoryStore{})
	service.Store = store
	return service
}

func activeLogin(origin, organization string) Login {
	login := savedLogin(origin)
	login.Organization = organization
	login.AccessToken = organization + "-access"
	login.RefreshToken = organization + "-refresh"
	login.ExpiresAt = time.Now().Add(time.Hour).UTC()
	return login
}

func TestOrganizationSelectionUsesExplicitTargetOnlyAccountAndDefault(t *testing.T) {
	a := activeLogin("https://api.example.test", "org_a")
	b := activeLogin(a.APIOrigin, "org_b")
	service := accountService(t, a, b)
	ctx := context.Background()
	if err := service.SetDefault(ctx, reference(a)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, flag, project, want string
	}{
		{"default", "", "", "org_a"},
		{"flag", "org_b", "", "org_b"},
		{"project overrides default", "", "org_b", "org_b"},
		{"matching flag and project", "org_b", "org_b", "org_b"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service.Organization = test.flag
			err := service.UseLogin(ctx, LoginTarget{Organization: test.project}, nil, func(login Login) error {
				if login.Organization != test.want || login.AccessToken != test.want+"-access" {
					t.Fatalf("wrong organization or token: %s", login.Organization)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	service.Organization = "org_a"
	err := service.UseLogin(ctx, LoginTarget{Organization: "org_b"}, nil, func(Login) error {
		t.Fatal("conflicting --org ran the operation")
		return nil
	})
	if problem.Safe(err).Code != "organization_mismatch" {
		t.Fatalf("unexpected mismatch error: %v", err)
	}
	service = accountService(t, b)
	if err := service.UseLogin(ctx, LoginTarget{}, nil, func(login Login) error {
		if login.Organization != b.Organization {
			t.Fatal("only saved organization was not selected")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMultipleOrganizationsRequireChoiceWithoutDefault(t *testing.T) {
	a := activeLogin("https://api.example.test", "org_a")
	b := activeLogin(a.APIOrigin, "org_b")
	service := accountService(t, a, b)
	ctx := context.Background()
	if _, err := service.loadLogin(ctx, LoginTarget{}); problem.Safe(err).Code != "organization_required" {
		t.Fatalf("headless selection did not require an organization: %v", err)
	}
	service.ChooseOrganization = func(context.Context, []LoginReference) (LoginReference, error) {
		return reference(b), nil
	}
	login, err := service.loadLogin(ctx, LoginTarget{})
	if err != nil || login != b {
		t.Fatalf("interactive choice was ignored: %v", err)
	}
}

func TestRenewalAndLogoutLeaveOtherOrganizationsUntouched(t *testing.T) {
	revoked := ""
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		if request.URL.Path == "/oauth/management/revoke" {
			revoked = request.Form.Get("token")
			return
		}
		if request.Form.Get("refresh_token") != "org_b-refresh" {
			t.Error("renewed the wrong organization's token")
		}
		sendTokens(writer, "org_b")
	}))
	defer server.Close()
	a := activeLogin(server.URL, "org_a")
	b := activeLogin(server.URL, "org_b")
	b.ExpiresAt = time.Now().Add(-time.Hour)
	service := accountService(t, a, b)
	ctx := context.Background()
	if err := service.SetDefault(ctx, reference(a)); err != nil {
		t.Fatal(err)
	}
	service.Organization = b.Organization
	if err := service.UseLogin(ctx, LoginTarget{}, nil, func(login Login) error {
		if login.AccessToken != "new-access" {
			t.Fatal("operation did not receive the renewed token")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Logout(ctx, ""); err != nil {
		t.Fatal(err)
	}
	saved, err := service.Store.Read()
	if err != nil || len(saved.Logins) != 1 || saved.Logins[0] != a || saved.Default != reference(a) || revoked != "new-refresh" {
		t.Fatalf("renewal/logout affected the other organization: %v", err)
	}
}

func TestLoginReplacesOnlyMatchingOriginAndOrganization(t *testing.T) {
	revoked := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		revoked = append(revoked, request.Form.Get("token"))
	}))
	defer server.Close()
	a := activeLogin(server.URL, "org_a")
	otherOrigin := activeLogin("https://different.example.test", "org_a")
	service := accountService(t, a, otherOrigin)
	b := activeLogin(server.URL, "org_b")
	if _, err := service.replaceLogin(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 0 {
		t.Fatal("adding an organization revoked another login")
	}
	replacement := a
	replacement.AccessToken = "replacement-access"
	if _, err := service.replaceLogin(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	saved, _ := service.Store.Read()
	unchanged, exists := saved.find(reference(otherOrigin))
	if len(saved.Logins) != 3 || !exists || unchanged != otherOrigin || len(revoked) != 1 || revoked[0] != a.RefreshToken {
		t.Fatal("replacement lost or revoked a different login")
	}
}

func TestLegacyLoginRemainsAvailableWhenAddingOrganization(t *testing.T) {
	keyring.MockInit()
	previous := activeLogin("https://api.example.test", "org_a")
	data, _ := json.Marshal(previous)
	if err := keyring.Set(credentialService, credentialAccount, string(data)); err != nil {
		t.Fatal(err)
	}
	service := testService(&memoryStore{})
	service.Store = CredentialStore{Directory: t.TempDir()}
	additional := activeLogin(previous.APIOrigin, "org_b")
	if _, err := service.replaceLogin(context.Background(), additional); err != nil {
		t.Fatal(err)
	}
	saved, err := service.Store.Read()
	actual, exists := saved.find(reference(previous))
	if err != nil || !exists || actual != previous || len(saved.Logins) != 2 || saved.Default != reference(previous) {
		t.Fatalf("legacy login was lost: %v", err)
	}
}

func TestRecoveryKeepsNewOrganizationAndStopsPinnedOperation(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh project", true: "pending operation"}[pinned], func(t *testing.T) {
			a := activeLogin("https://api.example.test", "org_a")
			a.RefreshBlocked = true
			b := activeLogin(a.APIOrigin, "org_b")
			service := accountService(t, a)
			service.RequestLogin = func(ctx context.Context, options LoginOptions) (LoginSummary, error) {
				if options.ExpectedOrganization != "" {
					t.Fatal("recovery restricted browser login to the old account")
				}
				result, err := service.replaceLogin(ctx, b)
				return result, err
			}
			target := LoginTarget{}
			if pinned {
				target.Organization = a.Organization
			}
			ran := false
			err := service.UseLogin(context.Background(), target, nil, func(login Login) error {
				ran = true
				if login.Organization != b.Organization {
					t.Fatal("fresh project used the old organization")
				}
				return nil
			})
			if pinned && (problem.Safe(err).Code != "target_mismatch" || ran) || !pinned && (err != nil || !ran) {
				t.Fatalf("incorrect recovery behavior: ran=%v error=%v", ran, err)
			}
			saved, err := service.Store.Read()
			if _, exists := saved.find(reference(b)); err != nil || !exists || len(saved.Logins) != 2 {
				t.Fatalf("new login was not retained: %v", err)
			}
		})
	}
}

func TestCredentialReadFailureDoesNotOverwriteSavedLogins(t *testing.T) {
	keyring.MockInitWithError(errors.New("locked keychain"))
	defer keyring.MockInit()
	service := testService(&memoryStore{})
	service.Store = CredentialStore{Directory: t.TempDir()}
	if err := service.saveLogin(activeLogin("https://api.example.test", "org_a")); problem.Safe(err).Code != "credential_store_unavailable" {
		t.Fatalf("credential read error was ignored: %v", err)
	}
}
