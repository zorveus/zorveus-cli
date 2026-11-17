package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
)

func TestMissingLoginSignsInAndResumesCommand(t *testing.T) {
	for _, command := range [][]string{{"whoami"}, {"apps", "list"}, {"plan"}} {
		for _, savedOtherOrg := range []bool{false, true} {
			name := strings.Join(command, "_")
			if savedOtherOrg {
				name += "_other_organization_saved"
			}
			t.Run(name, func(t *testing.T) {
				logins := []auth.Login{}
				if savedOtherOrg {
					logins = append(logins, savedOrganization("org_other"))
				}
				dependencies := organizationDependencies(t, logins...)
				dependencies.InputIsTerminal = true
				dependencies.Stdin = strings.NewReader("\nn\n")
				opened, exchanges, reads := 0, 0, 0
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
						if request.Form.Get("grant_type") != "authorization_code" {
							t.Fatal("missing login tried to refresh another organization's tokens")
						}
						exchanges++
						_ = json.NewEncoder(writer).Encode(api.Tokens{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", TokenType: "Bearer", ExpiresIn: 300, OrgID: "org_a", Scope: defaultLoginScopes})
					case "/me", "/apps":
						reads++
						if request.Header.Get("Authorization") != "Bearer fresh-access" {
							t.Fatal("command used another organization's saved credentials")
						}
						if request.URL.Path == "/apps" {
							_, _ = writer.WriteString(`{"apps":[]}`)
						} else {
							_, _ = writer.WriteString(`{"user":{"user_id":"user_a","email":"user@example.test"},"session":{"authenticated":true,"expires_at":"2099-01-01T00:00:00Z"}}`)
						}
					default:
						t.Fatalf("unexpected API request: %s %s", request.Method, request.URL.Path)
					}
					return writer.Result(), nil
				})
				arguments := append([]string{}, command...)
				if command[0] == "plan" {
					config := filepath.Join(t.TempDir(), "zorveus.json")
					if err := os.WriteFile(config, []byte(`{"version":1,"organizationId":"org_a","app":{"name":"Demo","allowedModels":["*"]}}`), 0600); err != nil {
						t.Fatal(err)
					}
					arguments = append(arguments, "--config", config)
				} else if savedOtherOrg {
					arguments = append(arguments, "--org", "org_a")
				}
				if !savedOtherOrg {
					arguments = append(arguments, "--local")
				}
				arguments = append(arguments, "--json")
				var stdout, stderr bytes.Buffer
				code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies)
				var result resultEnvelope
				if code != 0 || json.Unmarshal(stdout.Bytes(), &result) != nil || !result.OK || opened != 1 || exchanges != 1 || reads == 0 {
					t.Fatalf("missing login did not resume: code=%d opens=%d exchanges=%d reads=%d stdout=%s stderr=%s", code, opened, exchanges, reads, &stdout, &stderr)
				}
				if !strings.Contains(stderr.String(), "Press Enter") || strings.Contains(stderr.String(), "could not be renewed") {
					t.Fatal("missing-login explanation was absent or described a renewal failure")
				}
				saved, err := dependencies.Auth.Store.Read()
				if err != nil || len(saved.Logins) != len(logins)+1 {
					t.Fatalf("login lost another organization or was not saved: %v", err)
				}
				if strings.Contains(stdout.String()+stderr.String(), "fresh-access") || strings.Contains(stdout.String()+stderr.String(), "fresh-refresh") {
					t.Fatal("login output exposed tokens")
				}
			})
		}
	}
}

func TestMissingLoginCancellationAndHeadlessModeNeverOpenBrowser(t *testing.T) {
	for _, test := range []struct {
		name, input        string
		terminal, headless bool
		want               string
	}{
		{"canceled", "", true, false, "login_canceled"},
		{"piped input", "\n", false, false, "login_required"},
		{"noninteractive", "\n", true, true, "login_required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies := organizationDependencies(t)
			dependencies.InputIsTerminal = test.terminal
			dependencies.Stdin = strings.NewReader(test.input)
			dependencies.OpenBrowser = func(context.Context, string) error {
				t.Fatal("browser opened without interactive approval")
				return nil
			}
			dependencies.Auth.API.HTTP.Transport = requestHandler(func(*http.Request) (*http.Response, error) {
				t.Fatal("API request sent before signing in")
				return nil, nil
			})
			arguments := []string{"whoami", "--local", "--org", "org_a", "--json"}
			if test.headless {
				arguments = append(arguments, "--non-interactive")
			}
			var stdout, stderr bytes.Buffer
			if code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies); code != 3 || !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("unexpected missing-login result: %d %s %s", code, &stdout, &stderr)
			}
		})
	}
}

func TestFirstLoginUsesProductionDefaultsAndCustomDashboardCanBeEntered(t *testing.T) {
	for _, test := range []struct {
		name, origin, dashboard, input string
	}{
		{"production", "https://api.zorveus.com", "app.zorveus.com", "\nn\n"},
		{"custom", "https://api.example.test", "dashboard.example.test", "https://dashboard.example.test\n\nn\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies := organizationDependencies(t)
			dependencies.InputIsTerminal = true
			dependencies.Stdin = strings.NewReader(test.input)
			dependencies.OpenBrowser = func(_ context.Context, address string) error {
				approveLoginOnDashboard(t, address, test.dashboard)
				return nil
			}
			dependencies.Auth.API.HTTP.Transport = requestHandler(func(request *http.Request) (*http.Response, error) {
				if request.URL.Scheme+"://"+request.URL.Host != test.origin {
					t.Fatal("login changed the selected API origin")
				}
				writer := httptest.NewRecorder()
				if request.URL.Path == "/oauth/management/token" {
					_ = json.NewEncoder(writer).Encode(api.Tokens{AccessToken: "new-access", RefreshToken: "new-refresh", TokenType: "Bearer", ExpiresIn: 300, OrgID: "org_a", Scope: defaultLoginScopes})
				} else if request.URL.Path == "/me" {
					_, _ = writer.WriteString(`{"user":{"user_id":"user_a","email":"user@example.test"},"session":{"authenticated":true,"expires_at":"2099-01-01T00:00:00Z"}}`)
				} else {
					t.Fatalf("unexpected request: %s", request.URL.Path)
				}
				return writer.Result(), nil
			})
			arguments := []string{"whoami", "--json"}
			if test.name == "custom" {
				arguments = append(arguments, "--api-url", test.origin)
			}
			var stdout, stderr bytes.Buffer
			if code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies); code != 0 {
				t.Fatalf("sign-in did not resume: %d %s %s", code, &stdout, &stderr)
			}
		})
	}
}
