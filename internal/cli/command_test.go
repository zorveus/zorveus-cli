package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
)

type testStore struct {
	login   auth.Login
	present bool
	err     error
}

func (store *testStore) Load() (auth.Login, error) {
	if store.err != nil {
		return auth.Login{}, store.err
	}
	if !store.present {
		return auth.Login{}, auth.ErrNoLogin
	}
	return store.login, nil
}

func (store *testStore) Save(login auth.Login) error {
	store.login = login
	store.present = true
	return nil
}

func (store *testStore) Delete() error {
	store.present = false
	return nil
}

func testDependencies(store *testStore) Dependencies {
	return Dependencies{
		Auth: &auth.Service{
			API:   api.NewClient(),
			Store: store,
			Now:   time.Now,
			Lock:  func(context.Context) (func(), error) { return func() {}, nil },
		},
		OpenBrowser: func(context.Context, string) error {
			return errors.New("browser must not open in this test")
		},
	}
}

func TestJSONAppListReturnsOnlySafeFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer private-access" || request.URL.Query().Get("owner_org_id") != "org_a" {
			t.Error("request was not bound to the saved login")
		}
		_, _ = fmt.Fprint(writer, `{"apps":[{"app_id":"app_a","app_name":"Assistant","owner_org_id":"org_a","status":"active","allowed_models":[],"client_secret":"private-server-secret"}]}`)
	}))
	defer server.Close()
	store := &testStore{present: true, login: auth.Login{
		APIOrigin: server.URL, ClientID: api.ClientID, Organization: "org_a",
		AccessToken: "private-access", RefreshToken: "private-refresh",
		ExpiresAt: time.Now().Add(time.Minute), Scope: "apps:read",
	}}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"apps", "list", "--json"}, &stdout, &stderr, testDependencies(store))
	if code != 0 {
		t.Fatalf("exit code = %d, diagnostics = %s", code, stderr.String())
	}
	var result struct {
		Version int             `json:"version"`
		OK      bool            `json:"ok"`
		Result  auth.AppsResult `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Version != 1 || len(result.Result.Apps) != 1 || result.Result.Apps[0].ID != "app_a" {
		t.Fatal("unexpected app result")
	}
	if strings.Contains(stdout.String()+stderr.String(), "private-") {
		t.Fatal("command output exposed credentials or unapproved response fields")
	}
}

func TestNonInteractiveLoginNeverOpensBrowserOrReadsCredentials(t *testing.T) {
	for _, explicitFlag := range []bool{false, true} {
		store := &testStore{err: errors.New("credential store should not be read")}
		dependencies := testDependencies(store)
		dependencies.InputIsTerminal = explicitFlag
		opened := false
		dependencies.OpenBrowser = func(context.Context, string) error {
			opened = true
			return nil
		}
		arguments := []string{"login", "--json", "--no-browser"}
		if explicitFlag {
			arguments = append(arguments, "--non-interactive")
		}
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies)
		if code != 3 || opened || !strings.Contains(stdout.String(), "interactive_login_required") {
			t.Fatal("non-interactive login did not stop before browser authorization")
		}
	}
}

func TestErrorsHaveStableExitCodesAndOneJSONResult(t *testing.T) {
	cases := []struct {
		arguments []string
		store     *testStore
		code      int
		errorCode string
	}{
		{[]string{"--json", "apps", "list"}, &testStore{}, 3, "login_required"},
		{[]string{"--json", "apps", "list", "--limit", "0"}, &testStore{}, 2, "invalid_limit"},
		{[]string{"--json", "apps", "list", "private-input"}, &testStore{}, 2, "invalid_arguments"},
		{[]string{"--json", "apps", "list", "--private-flag"}, &testStore{}, 2, "invalid_arguments"},
		{[]string{"--json", "private-command"}, &testStore{}, 2, "invalid_arguments"},
		{[]string{"--json", "apps", "list", "--log-level", "private-level"}, &testStore{}, 2, "invalid_log_level"},
		{[]string{"--json", "apps", "list"}, &testStore{err: errors.New("private-credential-error")}, 1, "operation_failed"},
	}
	for _, test := range cases {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), test.arguments, &stdout, &stderr, testDependencies(test.store))
		if code != test.code {
			t.Fatalf("%v: exit code = %d, expected %d", test.arguments, code, test.code)
		}
		var result resultEnvelope
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("%v: expected exactly one JSON object: %v, diagnostics: %s", test.arguments, err, stderr.String())
		}
		if result.OK || result.Error == nil || result.Error.Code != test.errorCode {
			t.Fatalf("unexpected error result: %s", stdout.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), "private-") {
			t.Fatal("an error exposed raw input or a credential-store error")
		}
	}
}

func TestHumanAppNamesCannotEmitTerminalControlSequences(t *testing.T) {
	if strings.ContainsAny(terminalText("name\x1b[31m\t\n"), "\x1b\t\n") {
		t.Fatal("terminal text still contains control characters")
	}
}

type requestHandler func(*http.Request) (*http.Response, error)

func (handler requestHandler) RoundTrip(request *http.Request) (*http.Response, error) {
	return handler(request)
}

func TestLoginUsesEnvironmentDefaultsAndExplicitOverrides(t *testing.T) {
	cases := []struct {
		name      string
		arguments []string
		api       string
		dashboard string
	}{
		{"production", nil, "https://api.zorveus.com", "https://app.zorveus.com"},
		{"local", []string{"--local"}, "http://localhost:8000", "http://localhost:3000"},
		{"root dashboard", []string{"--dashboard-url", "https://zorveus.com"}, "https://api.zorveus.com", "https://zorveus.com"},
		{"custom deployment", []string{"--api-url", "https://api.example.test", "--dashboard-url", "https://dashboard.example.test"}, "https://api.example.test", "https://dashboard.example.test"},
		{"custom local ports", []string{"--local", "--api-url", "http://127.0.0.1:8010", "--dashboard-url", "http://127.0.0.1:3010"}, "http://127.0.0.1:8010", "http://127.0.0.1:3010"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := &testStore{}
			dependencies := testDependencies(store)
			dependencies.InputIsTerminal = true
			dependencies.Auth.API.HTTP.Transport = requestHandler(func(request *http.Request) (*http.Response, error) {
				if request.URL.Scheme+"://"+request.URL.Host != test.api {
					t.Error("token exchange used the wrong API origin")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"private-access","token_type":"Bearer","expires_in":120,"scope":"apps:read","org_id":"org_a"}`)),
					Header:     make(http.Header),
				}, nil
			})
			dependencies.OpenBrowser = func(ctx context.Context, address string) error {
				authorization, err := url.Parse(address)
				if err != nil {
					return err
				}
				if authorization.Scheme+"://"+authorization.Host != test.dashboard {
					t.Error("authorization used the wrong dashboard origin")
				}
				callback, err := url.Parse(authorization.Query().Get("redirect_uri"))
				if err != nil {
					return err
				}
				query := callback.Query()
				query.Set("state", authorization.Query().Get("state"))
				query.Set("code", "test-code")
				callback.RawQuery = query.Encode()
				response, err := http.Get(callback.String())
				if err != nil {
					return err
				}
				response.Body.Close()
				return nil
			}
			arguments := append([]string{"login", "--json", "--timeout", "2s"}, test.arguments...)
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies)
			if code != 0 || !store.present || store.login.APIOrigin != test.api {
				t.Fatalf("login did not save the selected API origin: exit %d, diagnostics %s", code, stderr.String())
			}
		})
	}
}

func TestLocalModeStillRejectsHTTPOutsideLoopback(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"login", "--local", "--api-url", "http://example.test", "--json"}, &stdout, &stderr, testDependencies(&testStore{}))
	if code != 2 || !strings.Contains(stdout.String(), "insecure_origin") {
		t.Fatal("local mode allowed credentials over HTTP outside loopback")
	}
}

func TestCompletionSuggestsCommandsAndFlagsWithoutAuthentication(t *testing.T) {
	cases := []struct {
		arguments []string
		expected  []string
	}{
		{[]string{"__complete", "lo"}, []string{"login", "logout"}},
		{[]string{"__complete", "apps", "l"}, []string{"list"}},
		{[]string{"__complete", "login", "--loc"}, []string{"--local"}},
	}
	for _, test := range cases {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), test.arguments, &stdout, &stderr, Dependencies{})
		if code != 0 {
			t.Fatalf("completion failed: %s", stderr.String())
		}
		for _, expected := range test.expected {
			found := false
			for _, line := range strings.Split(stdout.String(), "\n") {
				if strings.SplitN(line, "\t", 2)[0] == expected {
					found = true
				}
			}
			if !found {
				t.Fatalf("completion did not suggest %q: %s", expected, stdout.String())
			}
		}
	}
}

func TestGeneratedZshCompletionIsValidShellSource(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"completion", "zsh"}, &stdout, &stderr, Dependencies{})
	if code != 0 || stdout.Len() == 0 {
		t.Fatalf("could not generate completion source: %s", stderr.String())
	}
	command := exec.Command(zsh, "-n")
	command.Stdin = &stdout
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("zsh could not parse the completion script: %v\n%s", err, output)
	}
}
