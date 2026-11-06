package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/api"
)

type memoryStore struct {
	login      Login
	present    bool
	saveErr    error
	loadErr    error
	saves      int
	failSaveAt int
}

func (store *memoryStore) Load() (Login, error) {
	if store.loadErr != nil {
		return Login{}, store.loadErr
	}
	if !store.present {
		return Login{}, ErrNoLogin
	}
	return store.login, nil
}

func (store *memoryStore) Save(login Login) error {
	store.saves++
	if store.failSaveAt == store.saves {
		return errors.New("private-store-error")
	}
	if store.saveErr != nil {
		return store.saveErr
	}
	store.login = login
	store.present = true
	return nil
}

func (store *memoryStore) Delete() error {
	store.present = false
	return nil
}

func testService(store *memoryStore) *Service {
	var mutex sync.Mutex
	return &Service{
		API:   api.NewClient(),
		Store: store,
		Now:   time.Now,
		Lock: func(context.Context) (func(), error) {
			mutex.Lock()
			return mutex.Unlock, nil
		},
	}
}

func savedLogin(origin string) Login {
	return Login{
		APIOrigin:    origin,
		ClientID:     api.ClientID,
		Organization: "org_a",
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Add(-time.Minute),
		Scope:        "apps:read apps:update",
	}
}

func sendTokens(writer http.ResponseWriter, organization string) {
	_ = json.NewEncoder(writer).Encode(api.Tokens{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		OrgID:        organization,
		Scope:        "apps:read",
		TokenType:    "Bearer",
		ExpiresIn:    120,
	})
}

func TestConcurrentReadsRefreshOnceAndUseSavedReplacement(t *testing.T) {
	var refreshes atomic.Int32
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/oauth/management/token" {
			refreshes.Add(1)
			if err := request.ParseForm(); err != nil {
				t.Error(err)
			}
			if request.Form.Get("refresh_token") != "old-refresh" || request.Form.Get("client_id") != api.ClientID || request.Form.Get("grant_type") != "refresh_token" {
				t.Error("incorrect refresh request")
			}
			sendTokens(writer, "org_a")
			return
		}
		reads.Add(1)
		if request.Header.Get("Authorization") != "Bearer new-access" || request.URL.Query().Get("owner_org_id") != "org_a" {
			t.Error("read did not use the renewed login")
		}
		_, _ = writer.Write([]byte(`{"apps":[]}`))
	}))
	defer server.Close()
	store := &memoryStore{login: savedLogin(server.URL), present: true}
	service := testService(store)
	errors := make(chan error, 6)
	for range 6 {
		go func() {
			_, err := service.Apps(context.Background(), "", 100)
			errors <- err
		}()
	}
	for range 6 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if refreshes.Load() != 1 || reads.Load() != 6 || store.login.RefreshToken != "new-refresh" {
		t.Fatal("concurrent commands did not share the saved token replacement")
	}
}

func TestLostRefreshResponseIsNotRetriedByAnotherCommand(t *testing.T) {
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		refreshes.Add(1)
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()
	store := &memoryStore{login: savedLogin(server.URL), present: true}
	service := testService(store)
	for range 2 {
		if _, err := service.Apps(context.Background(), "", 100); err == nil {
			t.Fatal("read succeeded after a lost refresh response")
		}
	}
	if refreshes.Load() != 1 || !store.login.RefreshBlocked {
		t.Fatal("a potentially consumed refresh token was retried")
	}
}

func TestRefreshSaveFailurePreventsReuseOfConsumedToken(t *testing.T) {
	var refreshes atomic.Int32
	store := &memoryStore{present: true, failSaveAt: 2}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		refreshes.Add(1)
		sendTokens(writer, "org_a")
	}))
	defer server.Close()
	store.login = savedLogin(server.URL)
	service := testService(store)
	for range 2 {
		_, err := service.Apps(context.Background(), "", 100)
		if err == nil || strings.Contains(err.Error(), "private-store-error") {
			t.Fatalf("unexpected refresh error: %v", err)
		}
	}
	if refreshes.Load() != 1 {
		t.Fatal("refresh was retried after replacement could not be saved")
	}
}

func TestReadWithoutRenewalRequiresLoginAfterExpiry(t *testing.T) {
	store := &memoryStore{login: savedLogin("https://api.example.test"), present: true}
	store.login.RefreshToken = ""
	_, err := testService(store).Apps(context.Background(), "", 100)
	if err == nil || !strings.Contains(err.Error(), "renewal was not approved") {
		t.Fatalf("expected login requirement, got %v", err)
	}
}

func TestTargetMismatchAndMissingScopeFailBeforeNetwork(t *testing.T) {
	store := &memoryStore{login: savedLogin("https://api.example.test"), present: true}
	service := testService(store)
	if _, err := service.Apps(context.Background(), "https://other.example.test", 100); err == nil {
		t.Fatal("target mismatch was accepted")
	}
	store.login.Scope = "apps:update"
	if _, err := service.Apps(context.Background(), "", 100); err == nil || !strings.Contains(err.Error(), "apps:read") {
		t.Fatal("read without apps:read was accepted")
	}
}

func TestLogoutRetainsCredentialsWhenRevocationFails(t *testing.T) {
	var shouldFail atomic.Bool
	shouldFail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		if request.Form.Get("token") != "old-refresh" || request.Header.Get("Authorization") != "" {
			t.Error("logout did not revoke the current refresh token")
		}
		if shouldFail.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(writer, "{}")
	}))
	defer server.Close()
	store := &memoryStore{login: savedLogin(server.URL), present: true}
	service := testService(store)
	if _, err := service.Logout(context.Background(), ""); err == nil || !store.present {
		t.Fatal("failed revocation cleared credentials")
	}
	shouldFail.Store(false)
	if _, err := service.Logout(context.Background(), ""); err != nil || store.present {
		t.Fatal("successful revocation did not clear credentials")
	}
	if _, err := service.Logout(context.Background(), ""); err != nil {
		t.Fatal("logout without a saved login should succeed")
	}
}

func TestLogoutWithoutRefreshRevokesExpiredAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		if request.Form.Get("token") != "old-access" {
			t.Error("access token was not revoked")
		}
		_, _ = fmt.Fprint(writer, "{}")
	}))
	defer server.Close()
	store := &memoryStore{login: savedLogin(server.URL), present: true}
	store.login.RefreshToken = ""
	if _, err := testService(store).Logout(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
}

func TestUncertainRefreshCannotReportSuccessfulRemoteLogout(t *testing.T) {
	store := &memoryStore{login: savedLogin("https://api.example.test"), present: true}
	store.login.RefreshBlocked = true
	_, err := testService(store).Logout(context.Background(), "")
	if err == nil || !store.present || !strings.Contains(err.Error(), "dashboard") {
		t.Fatal("logout claimed to revoke a session whose current tokens are unknown")
	}
}
