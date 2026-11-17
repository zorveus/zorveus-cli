package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zalando/go-keyring"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestSessionReadCachesOrganizationNameForSignInAfterCredentialsDisappear(t *testing.T) {
	name := "My Startup"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/me" {
			t.Fatalf("name lookup made an unexpected request: %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"user":{"user_id":"user_a","email":"user@example.test"},"session":{"authenticated":true,"expires_at":"2099-01-01T00:00:00Z"},"orgs":[{"org_id":"org_a","org_name":"` + name + `"}]}`))
	}))
	defer server.Close()
	login := activeLogin(server.URL, "org_a")
	service := accountService(t, login)
	for _, updatedName := range []string{"My Startup", "Updated Startup"} {
		name = updatedName
		identity, err := service.Whoami(context.Background(), "")
		if err != nil || identity.OrganizationName != name || identity.Organization != "org_a" {
			t.Fatalf("session did not expose the organization name: %v", err)
		}
		saved, err := service.Store.Read()
		if err != nil || len(saved.Logins) != 1 || saved.Logins[0].OrganizationName != name || saved.Logins[0].AccessToken != login.AccessToken {
			t.Fatalf("name update changed identity or credentials: %v", err)
		}
	}
	if err := keyring.Delete(credentialService, loginAccount(reference(login))); err != nil {
		t.Fatal(err)
	}
	message := ""
	canceled := problem.New("login_canceled", "Canceled.", 3)
	service.RequestLogin = func(_ context.Context, options LoginOptions) (LoginSummary, error) {
		message = options.SignInMessage
		return LoginSummary{}, canceled
	}
	err := service.UseLogin(context.Background(), LoginTarget{APIOrigin: server.URL, Organization: "org_a"}, nil, func(Login) error {
		t.Fatal("operation ran before sign-in")
		return nil
	})
	if err != canceled || message != "Sign in to organization Updated Startup (org_a) to continue." {
		t.Fatalf("sign-in lost the cached name after credentials disappeared: %q %v", message, err)
	}
}
