package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func completeBrowserLogin(t *testing.T, address, outcome string) url.Values {
	t.Helper()
	authorization, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	parameters := authorization.Query()
	callback, err := url.Parse(parameters.Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	query := callback.Query()
	query.Set("state", parameters.Get("state"))
	if outcome == "deny" {
		query.Set("error", "access_denied")
	} else {
		query.Set("code", "one-time-code")
	}
	callback.RawQuery = query.Encode()
	response, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", response.StatusCode)
	}
	return parameters
}

func TestBrowserLoginUsesPKCEAndReplacesPreviousOrganization(t *testing.T) {
	verifiers := make(chan string, 1)
	revocations := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		if request.URL.Path == "/oauth/management/revoke" {
			revocations <- request.Form.Get("token")
			_, _ = writer.Write([]byte(`{}`))
			return
		}
		if request.Form.Get("code") != "one-time-code" || request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("redirect_uri") == "" {
			t.Error("invalid code exchange")
		}
		if request.Form.Get("client_secret") != "" || request.Header.Get("Authorization") != "" {
			t.Error("public client sent client credentials")
		}
		verifiers <- request.Form.Get("code_verifier")
		sendTokens(writer, "org_b")
	}))
	defer server.Close()
	store := &memoryStore{login: savedLogin(server.URL), present: true}
	service := testService(store)
	var authorization url.Values
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := service.Login(ctx, LoginOptions{
		APIOrigin:       server.URL,
		DashboardOrigin: "https://dashboard.example.test",
		Scope:           "apps:read apps:update",
		OpenBrowser: func(ctx context.Context, address string) {
			authorization = completeBrowserLogin(t, address, "approve")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if authorization.Get("code_challenge") != oauth2.S256ChallengeFromVerifier(<-verifiers) || authorization.Get("code_challenge_method") != "S256" {
		t.Fatal("code exchange did not prove the browser's PKCE challenge")
	}
	if authorization.Get("renewal_allowed") != "true" || authorization.Get("org_id") != "" || authorization.Get("credential_type") != "management" || len(authorization.Get("state")) < 16 {
		t.Fatal("incorrect browser authorization parameters")
	}
	if <-revocations != "old-refresh" || store.login.Organization != "org_b" || store.login.AccessToken != "new-access" || summary.Organization != "org_b" {
		t.Fatal("new login did not replace the old organization and session")
	}
	response, callbackError := http.Get(authorization.Get("redirect_uri"))
	if callbackError == nil {
		response.Body.Close()
		t.Fatal("callback listener remained open after login")
	}
}

func TestDeniedAndCanceledLoginPreservePreviousCredentials(t *testing.T) {
	for _, outcome := range []string{"deny", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			previous := savedLogin("https://api.example.test")
			store := &memoryStore{login: previous, present: true}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := testService(store).Login(ctx, LoginOptions{
				APIOrigin:       previous.APIOrigin,
				DashboardOrigin: "https://dashboard.example.test",
				Scope:           "apps:read",
				OpenBrowser: func(ctx context.Context, address string) {
					if outcome == "cancel" {
						cancel()
						return
					}
					completeBrowserLogin(t, address, "deny")
				},
			})
			if err == nil || store.login != previous || store.saves != 0 {
				t.Fatal("an unsuccessful browser authorization changed the login")
			}
		})
	}
}

func TestCallbackRejectsInvalidStateOnSuccessAndDenial(t *testing.T) {
	result := make(chan callbackResult, 1)
	handler := callbackHandler("127.0.0.1:43210", "expected-state", result)
	invalidQueries := []string{
		"code=private-code&state=wrong-state",
		"error=access_denied&state=wrong-state",
		"code=private-code&state=expected-state&state=expected-state",
		"code=private-code&error=access_denied&state=expected-state",
		"code=one&code=two&state=expected-state",
	}
	for _, query := range invalidQueries {
		writer := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:43210/callback?"+query, nil)
		handler.ServeHTTP(writer, request)
		if writer.Code != http.StatusBadRequest || strings.Contains(writer.Body.String(), "private-code") {
			t.Fatal("invalid callback was accepted or exposed the code")
		}
		select {
		case <-result:
			t.Fatal("invalid callback completed login")
		default:
		}
	}
	writer := httptest.NewRecorder()
	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:43210/callback?code=valid&state=expected-state", nil))
	if writer.Code != http.StatusOK || (<-result).code != "valid" {
		t.Fatal("a valid callback after invalid requests did not complete login")
	}
}

func TestReplacementRevocationFailureKeepsOldLoginAndRevokesNewSession(t *testing.T) {
	revocations := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		if request.URL.Path == "/oauth/management/token" {
			sendTokens(writer, "org_b")
			return
		}
		token := request.Form.Get("token")
		revocations <- token
		if token == "old-refresh" {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()
	previous := savedLogin(server.URL)
	store := &memoryStore{login: previous, present: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := testService(store).Login(ctx, LoginOptions{
		APIOrigin:       server.URL,
		DashboardOrigin: "https://dashboard.example.test",
		Scope:           "apps:read",
		OpenBrowser: func(ctx context.Context, address string) {
			completeBrowserLogin(t, address, "approve")
		},
	})
	if err == nil || store.login != previous || <-revocations != "old-refresh" || <-revocations != "new-refresh" {
		t.Fatal("failed replacement lost the previous login or abandoned the new session")
	}
}

func TestReplacementSaveFailureExplainsThatPreviousLoginWasRevoked(t *testing.T) {
	revocations := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		revocations <- request.Form.Get("token")
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()
	store := &memoryStore{login: savedLogin(server.URL), present: true, saveErr: errors.New("private-store-error")}
	replacement := savedLogin(server.URL)
	replacement.RefreshToken = "new-refresh"
	_, err := testService(store).replaceLogin(context.Background(), replacement)
	if err == nil || !strings.Contains(err.Error(), "already been revoked") || strings.Contains(err.Error(), "private-store-error") {
		t.Fatalf("unclear replacement failure: %v", err)
	}
	if <-revocations != "old-refresh" || <-revocations != "new-refresh" {
		t.Fatal("replacement failure did not clean up both sessions")
	}
}

func TestLoginWithoutRenewalSavesOnlyTheAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"access_token":"short-lived-access","token_type":"Bearer","expires_in":120,"scope":"apps:read","org_id":"org_a"}`))
	}))
	defer server.Close()
	store := &memoryStore{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := testService(store).Login(ctx, LoginOptions{
		APIOrigin:       server.URL,
		DashboardOrigin: "https://dashboard.example.test",
		Scope:           "apps:read",
		OpenBrowser: func(ctx context.Context, address string) {
			completeBrowserLogin(t, address, "approve")
		},
	})
	if err != nil || summary.Renewal || !store.present || store.login.RefreshToken != "" {
		t.Fatalf("login without renewal was not saved correctly: %v", err)
	}
}

func TestRecoveryRejectsDifferentAccountBeforeReplacingSavedLogin(t *testing.T) {
	for _, changed := range []string{"organization", "user"} {
		t.Run(changed, func(t *testing.T) {
			revoked := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_ = request.ParseForm()
				switch request.URL.Path {
				case "/oauth/management/token":
					organization := "org_a"
					if changed == "organization" {
						organization = "org_b"
					}
					sendTokens(writer, organization)
				case "/me":
					_, _ = writer.Write([]byte(`{"user":{"user_id":"other_user","email":"other@example.test"},"session":{"authenticated":true,"expires_at":"2099-01-01T00:00:00Z"}}`))
				case "/oauth/management/revoke":
					revoked = append(revoked, request.Form.Get("token"))
				}
			}))
			defer server.Close()
			previous := savedLogin(server.URL)
			store := &memoryStore{present: true, login: previous}
			_, err := testService(store).Login(context.Background(), LoginOptions{
				APIOrigin: server.URL, DashboardOrigin: "https://dashboard.example.test", Scope: "apps:read",
				ExpectedOrganization: "org_a", ExpectedUser: "original_user",
				OpenBrowser: func(_ context.Context, address string) { completeBrowserLogin(t, address, "approve") },
			})
			if err == nil || store.login != previous || len(revoked) != 1 || revoked[0] != "new-refresh" {
				t.Fatalf("wrong account replaced the saved login: error=%v revoked=%v", err, revoked)
			}
		})
	}
}
