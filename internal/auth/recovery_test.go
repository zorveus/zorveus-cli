package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestRenewalRecoveryContinuesOnceAfterUnlockingCredentials(t *testing.T) {
	for _, failure := range []string{"refresh_rejected", "blocked", "no_renewal", "invalid_response", "save_failed"} {
		t.Run(failure, func(t *testing.T) {
			refreshes := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				refreshes++
				if failure == "save_failed" {
					sendTokens(writer, "org_a")
					return
				}
				if failure == "invalid_response" {
					sendTokens(writer, "other_org")
					return
				}
				writer.WriteHeader(http.StatusUnauthorized)
			}))
			defer server.Close()
			store := &memoryStore{present: true, login: savedLogin(server.URL)}
			store.login.DashboardOrigin = "https://dashboard.example.test"
			store.login.RefreshBlocked = failure == "blocked"
			if failure == "no_renewal" {
				store.login.RefreshToken = ""
			}
			if failure == "save_failed" {
				store.failSaveAt = 2
			}
			service := testService(store)
			locked := make(chan struct{}, 1)
			service.Lock = func(ctx context.Context) (func(), error) {
				select {
				case locked <- struct{}{}:
					return func() { <-locked }, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			logins, operations := 0, 0
			service.RequestLogin = func(ctx context.Context, options LoginOptions) error {
				logins++
				if options.APIOrigin != server.URL || options.DashboardOrigin != store.login.DashboardOrigin || options.Scope != store.login.Scope || options.ExpectedOrganization != "org_a" {
					t.Fatal("recovery lost the saved login target or permissions")
				}
				unlock, err := service.Lock(ctx)
				if err != nil {
					return err
				}
				defer unlock()
				fresh := savedLogin(server.URL)
				fresh.AccessToken, fresh.RefreshToken = "browser-access", "browser-refresh"
				fresh.ExpiresAt = time.Now().Add(time.Hour)
				return store.Save(fresh)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := service.UseLogin(ctx, LoginTarget{APIOrigin: server.URL, Organization: "org_a"}, []string{"apps:read"}, func(login Login) error {
				operations++
				if login.AccessToken != "browser-access" {
					t.Fatal("operation used the expired credentials")
				}
				return nil
			})
			if err != nil || logins != 1 || operations != 1 {
				t.Fatalf("recovery did not continue once: logins=%d operations=%d error=%v", logins, operations, err)
			}
			wantRefreshes := 1
			if failure == "blocked" || failure == "no_renewal" {
				wantRefreshes = 0
			}
			if refreshes != wantRefreshes {
				t.Fatalf("refreshes=%d, want %d", refreshes, wantRefreshes)
			}
		})
	}
}

func TestCanceledRecoveryPreservesBlockedLoginAndDoesNotRunOperation(t *testing.T) {
	store := &memoryStore{present: true, login: savedLogin("https://api.example.test")}
	store.login.RefreshBlocked = true
	previous := store.login
	service := testService(store)
	canceled := problem.New("login_canceled", "Login canceled.", 3)
	service.RequestLogin = func(context.Context, LoginOptions) error { return canceled }
	err := service.UseLogin(context.Background(), LoginTarget{}, nil, func(Login) error {
		t.Fatal("operation ran after canceled login")
		return nil
	})
	if err != canceled || store.login != previous {
		t.Fatal("cancellation changed the login or hid the browser error")
	}
}

func TestRecoveryRejectsChangedTargetBeforeRunningOperation(t *testing.T) {
	for _, changed := range []string{"origin", "organization", "user"} {
		t.Run(changed, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, _ = writer.Write([]byte(`{"user":{"user_id":"different_user","email":"other@example.test"},"session":{"authenticated":true,"expires_at":"2099-01-01T00:00:00Z"}}`))
			}))
			defer server.Close()
			store := &memoryStore{present: true, login: savedLogin(server.URL)}
			store.login.RefreshBlocked = true
			service := testService(store)
			service.RequestLogin = func(_ context.Context, options LoginOptions) error {
				if options.ExpectedUser != "original_user" {
					t.Fatal("recovery did not pin the operation's user")
				}
				fresh := savedLogin(server.URL)
				fresh.ExpiresAt = time.Now().Add(time.Hour)
				if changed == "origin" {
					fresh.APIOrigin = "https://different.example.test"
				}
				if changed == "organization" {
					fresh.Organization = "other_org"
				}
				return store.Save(fresh)
			}
			err := service.UseLogin(context.Background(), LoginTarget{UserID: "original_user"}, nil, func(Login) error {
				t.Fatal("operation ran against a different target")
				return nil
			})
			if problem.Safe(err).Code != "target_mismatch" {
				t.Fatalf("unexpected target error: %v", err)
			}
		})
	}
}

func TestOperationErrorNeverRestartsLoginOrReplaysOperation(t *testing.T) {
	store := &memoryStore{present: true, login: savedLogin("https://api.example.test")}
	store.login.ExpiresAt = time.Now().Add(time.Hour)
	service := testService(store)
	service.RequestLogin = func(context.Context, LoginOptions) error {
		t.Fatal("an operation error started browser login")
		return nil
	}
	writes := 0
	uncertain := errors.New("the mutation response was lost")
	err := service.UseLogin(context.Background(), LoginTarget{}, nil, func(Login) error {
		writes++
		return uncertain
	})
	if err != uncertain || writes != 1 {
		t.Fatal("an uncertain mutation was replayed")
	}
}

func TestRenewalPreservesDashboardOrigin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sendTokens(writer, "org_a")
	}))
	defer server.Close()
	store := &memoryStore{present: true, login: savedLogin(server.URL)}
	store.login.DashboardOrigin = "https://custom-dashboard.example.test"
	err := testService(store).UseLogin(context.Background(), LoginTarget{}, nil, func(Login) error { return nil })
	if err != nil || store.login.DashboardOrigin != "https://custom-dashboard.example.test" {
		t.Fatalf("renewal lost the dashboard origin: %v", err)
	}
}

func TestRecoveryDoesNotLoopWhenReplacementAlsoNeedsLogin(t *testing.T) {
	store := &memoryStore{present: true, login: savedLogin("https://api.example.test")}
	store.login.RefreshBlocked = true
	service := testService(store)
	logins := 0
	service.RequestLogin = func(context.Context, LoginOptions) error { logins++; return nil }
	err := service.UseLogin(context.Background(), LoginTarget{}, nil, func(Login) error {
		t.Fatal("operation ran without a usable replacement")
		return nil
	})
	if err == nil || logins != 1 {
		t.Fatal("recovery kept requesting a login")
	}
}
