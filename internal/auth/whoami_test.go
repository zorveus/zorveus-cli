package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWhoamiRenewsLoginAndUsesTokenOrganizationAndScopes(t *testing.T) {
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/oauth/management/token" {
			refreshes.Add(1)
			_, _ = fmt.Fprint(writer, `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":120,"scope":"routing_keys:read","org_id":"org_a"}`)
			return
		}
		if request.URL.Path != "/me" || request.Header.Get("Authorization") != "Bearer new-access" {
			t.Error("whoami did not use /me with the renewed access token")
		}
		_, _ = fmt.Fprint(writer, `{
			"user":{"user_id":"usr_a","email":"person@example.test","first_name":"Person"},
			"default_org_id":"org_default","orgs":[{"org_id":"org_a"}],
			"session":{"authenticated":true,"expires_at":"2030-01-01T00:00:00Z"}
		}`)
	}))
	defer server.Close()
	login := savedLogin(server.URL)
	login.Scope = "routing_keys:read routing_keys:write"
	store := &memoryStore{login: login, present: true}
	result, err := testService(store).Whoami(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.User.ID != "usr_a" || result.Organization != "org_a" || result.Scope != "routing_keys:read" {
		t.Fatal("whoami did not use the selected organization and currently issued scopes")
	}
	if refreshes.Load() != 1 || store.login.RefreshToken != "new-refresh" {
		t.Fatal("whoami did not coordinate and persist token renewal")
	}
}

func TestWhoamiRejectsAnotherOriginBeforeSendingCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Error("target mismatch must stop before an API request")
	}))
	defer server.Close()
	login := savedLogin(server.URL)
	login.ExpiresAt = time.Now().Add(time.Minute)
	store := &memoryStore{login: login, present: true}
	_, err := testService(store).Whoami(context.Background(), "https://another.example.test")
	if err == nil {
		t.Fatal("whoami accepted another API origin")
	}
}
