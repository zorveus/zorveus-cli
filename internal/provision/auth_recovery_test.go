package provision

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

type recoveryTransport func(*http.Request) (*http.Response, error)

func (transport recoveryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestApplyContinuesAfterRenewalFailsBetweenResourceCreations(t *testing.T) {
	service, backend, store, config, path := setup(t)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	store.login.RefreshToken = "old-refresh"
	now := time.Now()
	service.Auth.Now = func() time.Time {
		if len(backend.writes) > 0 {
			return now.Add(2 * time.Hour)
		}
		return now
	}
	refreshes, logins := 0, 0
	service.Auth.API.HTTP.Transport = recoveryTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/oauth/management/token" {
			return backend.RoundTrip(request)
		}
		refreshes++
		writer := httptest.NewRecorder()
		writer.WriteHeader(http.StatusUnauthorized)
		return writer.Result(), nil
	})
	service.Auth.RequestLogin = func(_ context.Context, options auth.LoginOptions) error {
		logins++
		if options.ExpectedOrganization != "org_a" || options.ExpectedUser != "user_a" {
			t.Fatal("apply lost the approved target during login")
		}
		fresh := store.login
		fresh.RefreshBlocked = false
		fresh.RefreshToken = "fresh-refresh"
		fresh.ExpiresAt = service.Auth.Now().Add(time.Hour)
		return store.Save(fresh)
	}
	if _, err := service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /apps", "POST /app-connections/dashboard", "PATCH /app-connections/conn_1/cap", "POST /app-connections/dashboard", "POST /orgs/org_a/apps/app_a/routing-keys"}
	if refreshes != 1 || logins != 1 || !slices.Equal(backend.writes, want) {
		t.Fatalf("apply restarted or repeated a write: refreshes=%d logins=%d writes=%v", refreshes, logins, backend.writes)
	}
}

func TestCanceledLoginBeforeCreationLeavesTheStepSafeToResume(t *testing.T) {
	service, backend, store, config, path := setup(t)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	expireBeforeCreate := true
	service.Auth.Now = func() time.Time {
		// Trigger expiration only after apply has saved its journal.
		if expireBeforeCreate {
			var plan Plan
			_, _, _ = loadJSON(path, &plan)
			var journal Journal
			if exists, _, _ := loadJSON(journalPath(plan), &journal); exists {
				return time.Now().Add(2 * time.Hour)
			}
		}
		return time.Now()
	}
	service.Auth.RequestLogin = func(context.Context, auth.LoginOptions) error {
		return problem.New("login_canceled", "Login canceled.", 3)
	}
	_, err := service.Apply(context.Background(), "", path, true, false)
	if problem.Safe(err).Code != "login_canceled" || len(backend.writes) != 0 {
		t.Fatalf("creation ran after canceled login: error=%v writes=%v", err, backend.writes)
	}
	expireBeforeCreate = false
	store.login.ExpiresAt = time.Now().Add(time.Hour)
	if _, err := service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatalf("a request that was never sent blocked resumption: %v", err)
	}
}
