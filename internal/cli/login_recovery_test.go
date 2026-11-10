package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

type loginPromptWriter struct{ prompts chan struct{} }

func (writer loginPromptWriter) Write(value []byte) (int, error) {
	if strings.Contains(string(value), "Press Enter") {
		writer.prompts <- struct{}{}
	}
	return len(value), nil
}

func TestLoginApprovalWaitsForEnter(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	prompts := make(chan struct{}, 2)
	finished := make(chan error, 1)
	go func() {
		finished <- waitForLoginApproval(ctx, bufio.NewReader(reader), terminalUI{output: loginPromptWriter{prompts}})
	}()
	for _, answer := range []string{"not yet\n", "\n"} {
		select {
		case <-prompts:
		case <-ctx.Done():
			t.Fatal("login approval did not display its prompt")
		}
		select {
		case err := <-finished:
			t.Fatalf("login continued before Enter: %v", err)
		default:
		}
		if _, err := io.WriteString(writer, answer); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("login did not continue after Enter")
	}
}

func TestLoginApprovalCanBeCanceledWhileWaitingForInput(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prompts := make(chan struct{}, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- waitForLoginApproval(ctx, bufio.NewReader(reader), terminalUI{output: loginPromptWriter{prompts}})
	}()
	select {
	case <-prompts:
	case <-time.After(3 * time.Second):
		t.Fatal("login approval did not display its prompt")
	}
	cancel()
	select {
	case err := <-finished:
		if problem.Safe(err).Code != "login_canceled" {
			t.Fatalf("unexpected cancellation error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("login approval kept waiting after cancellation")
	}
}

func TestLoginApprovalLeavesFollowingInputUnread(t *testing.T) {
	input := strings.NewReader("\napplication input\n")
	if err := waitForLoginApproval(context.Background(), input, terminalUI{output: io.Discard}); err != nil {
		t.Fatal(err)
	}
	remaining, err := io.ReadAll(input)
	if err != nil || string(remaining) != "application input\n" {
		t.Fatalf("login approval consumed subsequent input: %q %v", remaining, err)
	}
}

func TestRecoveryStopsBeforeOpeningBrowserWhenInputCloses(t *testing.T) {
	previous := auth.Login{APIOrigin: "https://api.zorveus.com", ClientID: api.ClientID, Organization: "org_a", AccessToken: "blocked", RefreshBlocked: true, Scope: defaultLoginScopes, ExpiresAt: time.Now().Add(time.Minute)}
	store := &testStore{present: true, login: previous}
	dependencies := testDependencies(store)
	dependencies.InputIsTerminal = true
	dependencies.Stdin = strings.NewReader("")
	dependencies.OpenBrowser = func(context.Context, string) error {
		t.Fatal("browser opened without Enter")
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"whoami", "--json"}, &stdout, &stderr, dependencies)
	if code != 3 || !strings.Contains(stdout.String(), "login_canceled") || store.login != previous {
		t.Fatalf("closed input did not cancel recovery safely: %d %s %s", code, &stdout, &stderr)
	}
}

func approveRecoveryLogin(t *testing.T, address string) {
	t.Helper()
	authorization, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.Host != "localhost:3000" {
		t.Fatalf("login opened the wrong dashboard: %s", authorization.Host)
	}
	callback, err := url.Parse(authorization.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	query := callback.Query()
	query.Set("state", authorization.Query().Get("state"))
	query.Set("code", "browser-code")
	callback.RawQuery = query.Encode()
	response, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", response.StatusCode)
	}
}

func TestInteractiveCommandsRecoverLoginAndKeepJSONOutputClean(t *testing.T) {
	for _, command := range [][]string{{"whoami"}, {"apps", "list"}, {"plan"}} {
		t.Run(strings.Join(command, "_"), func(t *testing.T) {
			store := &testStore{present: true, login: auth.Login{
				APIOrigin: "http://localhost:8000", ClientID: api.ClientID, Organization: "org_a",
				AccessToken: "expired-access", RefreshToken: "old-refresh", Scope: defaultLoginScopes,
				ExpiresAt: time.Now().Add(-time.Minute),
			}}
			dependencies := testDependencies(store)
			dependencies.InputIsTerminal = true
			dependencies.Stdin = strings.NewReader("\n")
			opened, refreshes, exchanges, reads := 0, 0, 0, 0
			dependencies.OpenBrowser = func(_ context.Context, address string) error {
				opened++
				approveRecoveryLogin(t, address)
				return nil
			}
			dependencies.Auth.API.HTTP.Transport = requestHandler(func(request *http.Request) (*http.Response, error) {
				writer := httptest.NewRecorder()
				switch request.URL.Path {
				case "/oauth/management/token":
					_ = request.ParseForm()
					if request.Form.Get("grant_type") == "refresh_token" {
						refreshes++
						writer.WriteHeader(http.StatusUnauthorized)
						break
					}
					exchanges++
					_ = json.NewEncoder(writer).Encode(api.Tokens{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", TokenType: "Bearer", ExpiresIn: 300, OrgID: "org_a", Scope: defaultLoginScopes})
				case "/oauth/management/revoke":
					// Recovery must still work if the consumed refresh token cannot be revoked.
					writer.WriteHeader(http.StatusUnauthorized)
				case "/me", "/apps":
					reads++
					if request.Header.Get("Authorization") != "Bearer fresh-access" {
						t.Fatal("command used stale credentials after login")
					}
					if request.URL.Path == "/apps" {
						_, _ = writer.WriteString(`{"apps":[]}`)
					} else {
						_, _ = writer.WriteString(`{"user":{"user_id":"user_a","email":"user@example.test"},"session":{"authenticated":true,"expires_at":"2099-01-01T00:00:00Z"},"orgs":[{"org_id":"org_a"}]}`)
					}
				default:
					t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
				}
				return writer.Result(), nil
			})
			arguments := append([]string{}, command...)
			if command[0] == "plan" {
				config := filepath.Join(t.TempDir(), "zorveus.json")
				if err := os.WriteFile(config, []byte(`{"version":1,"app":{"name":"Demo","allowedModels":["*"]}}`), 0600); err != nil {
					t.Fatal(err)
				}
				arguments = append(arguments, "--config", config)
			}
			arguments = append(arguments, "--json")
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies)
			var envelope resultEnvelope
			if code != 0 || json.Unmarshal(stdout.Bytes(), &envelope) != nil || !envelope.OK || opened != 1 || refreshes != 1 || exchanges != 1 || reads == 0 {
				t.Fatalf("recovery failed: code=%d opened=%d refreshes=%d exchanges=%d reads=%d stdout=%s stderr=%s", code, opened, refreshes, exchanges, reads, &stdout, &stderr)
			}
			if store.login.DashboardOrigin != "http://localhost:3000" || dependencies.Auth.RequestLogin != nil {
				t.Fatal("recovery lost its dashboard or changed shared dependencies")
			}
			if strings.Contains(stdout.String()+stderr.String(), "fresh-access") || strings.Contains(stdout.String()+stderr.String(), "fresh-refresh") {
				t.Fatal("recovery exposed credentials")
			}
		})
	}
}

func TestHeadlessCommandsNeverStartRecoveryLogin(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		store := &testStore{present: true, login: auth.Login{APIOrigin: "https://api.zorveus.com", ClientID: api.ClientID, Organization: "org_a", AccessToken: "blocked", RefreshBlocked: true, Scope: defaultLoginScopes, ExpiresAt: time.Now().Add(time.Minute)}}
		dependencies := testDependencies(store)
		dependencies.InputIsTerminal = terminal
		dependencies.OpenBrowser = func(context.Context, string) error { t.Fatal("headless command opened browser"); return nil }
		arguments := []string{"whoami", "--json"}
		if terminal {
			arguments = append(arguments, "--non-interactive")
		}
		var stdout, stderr bytes.Buffer
		if code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies); code != 3 || !strings.Contains(stdout.String(), "login_required") {
			t.Fatalf("headless authentication changed: %d %s %s", code, &stdout, &stderr)
		}
	}
}

func TestSetupResumesAfterLoginExpiresWhileEnteringAnswers(t *testing.T) {
	config := filepath.Join(t.TempDir(), "zorveus.json")
	dependencies := wizardDependencies(t)
	store := dependencies.Auth.Store.(*testStore)
	store.login.DashboardOrigin = "http://localhost:3000"
	store.login.RefreshToken = "old-refresh"
	currentTime := time.Now()
	dependencies.Auth.Now = func() time.Time {
		if _, err := os.Stat(config); err == nil {
			return currentTime.Add(2 * time.Hour)
		}
		return currentTime
	}
	identity := dependencies.Auth.API.HTTP.Transport
	refreshes, opened := 0, 0
	dependencies.OpenBrowser = func(_ context.Context, address string) error {
		opened++
		approveRecoveryLogin(t, address)
		return nil
	}
	dependencies.Auth.API.HTTP.Transport = requestHandler(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/me" {
			return identity.RoundTrip(request)
		}
		writer := httptest.NewRecorder()
		switch request.URL.Path {
		case "/oauth/management/token":
			_ = request.ParseForm()
			if request.Form.Get("grant_type") == "refresh_token" {
				refreshes++
				writer.WriteHeader(http.StatusUnauthorized)
			} else {
				_ = json.NewEncoder(writer).Encode(api.Tokens{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", TokenType: "Bearer", ExpiresIn: 300, OrgID: "org_a", Scope: defaultLoginScopes})
			}
		case "/oauth/management/revoke":
			writer.WriteHeader(http.StatusUnauthorized)
		default:
			t.Fatalf("setup performed an unexpected request: %s %s", request.Method, request.URL.Path)
		}
		return writer.Result(), nil
	})
	dependencies.Stdin = strings.NewReader("My saved app\n\n1000\nPlus\n10\nn\ny\n\nn\n")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"setup", "--config", config}, &stdout, &stderr, dependencies)
	if code != 0 || opened != 1 || refreshes != 1 || strings.Count(stdout.String(), "App name") != 1 || !strings.Contains(stdout.String(), "Plan saved. No changes applied.") {
		t.Fatalf("setup did not resume: code=%d opened=%d refreshes=%d stdout=%s stderr=%s", code, opened, refreshes, &stdout, &stderr)
	}
	data, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(data), "My saved app") {
		t.Fatal("setup lost the wizard answers")
	}
}
