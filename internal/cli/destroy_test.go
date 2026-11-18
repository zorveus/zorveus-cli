package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
)

type destroyTransport struct {
	t       *testing.T
	deleted bool
	writes  int
}

func (transport *destroyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	status := http.StatusOK
	body := ""
	switch request.Method + " " + request.URL.Path {
	case "GET /me":
		body = `{"user":{"user_id":"user_a","email":"user@example.test"},"session":{"authenticated":true,"expires_at":"2099-01-01T00:00:00Z"},"orgs":[{"org_id":"org_a"}]}`
	case "GET /apps/app_a":
		body = `{"app":{"app_id":"app_a","app_name":"Demo","owner_org_id":"org_a","status":"active","allowed_models":["*"]}}`
		if transport.deleted {
			status, body = http.StatusForbidden, `{}`
		}
	case "GET /app-connections":
		body = `{"app_connections":[]}`
	case "GET /orgs/org_a/routing-keys":
		body = `{"items":[]}`
	case "DELETE /apps/app_a":
		transport.deleted = true
		transport.writes++
		body = `{"app_id":"app_a","deleted":true}`
	default:
		transport.t.Fatalf("unexpected destroy request: %s %s", request.Method, request.URL.Path)
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestDestroyCommandRequiresApprovalAndSupportsPreview(t *testing.T) {
	for _, mode := range []string{"preview", "no approval", "declined", "confirmed"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			config := filepath.Join(directory, "zorveus.prod.json")
			if err := os.WriteFile(config, []byte(`{"version":1,"organizationId":"org_a","app":{"id":"app_a","name":"Demo","allowedModels":["*"]}}`), 0600); err != nil {
				t.Fatal(err)
			}
			store := &testStore{present: true, login: auth.Login{APIOrigin: "https://api.example.test", ClientID: api.ClientID, Organization: "org_a", AccessToken: "private-token", Scope: defaultLoginScopes, ExpiresAt: time.Now().Add(time.Hour)}}
			dependencies := testDependencies(store)
			transport := &destroyTransport{t: t}
			dependencies.Auth.API.HTTP.Transport = transport
			arguments := []string{"destroy", "--config", config}
			expected := 0
			switch mode {
			case "preview":
				arguments = append(arguments, "--dry-run", "--json", "--non-interactive")
			case "no approval":
				arguments = append(arguments, "--json", "--non-interactive")
				expected = 2
			case "declined":
				dependencies.InputIsTerminal = true
				dependencies.Stdin = strings.NewReader("n\n")
			case "confirmed":
				arguments = append(arguments, "--yes", "--json", "--non-interactive")
			}
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), arguments, &stdout, &stderr, dependencies)
			if code != expected {
				t.Fatalf("unexpected destroy result: code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
			}
			if mode == "confirmed" && (!transport.deleted || transport.writes != 1 || !strings.Contains(stdout.String(), `"destroyed":true`)) {
				t.Fatal("confirmed destroy did not delete exactly the selected app")
			}
			if mode != "confirmed" && (transport.deleted || transport.writes != 0) {
				t.Fatal("destroy mutated resources without confirmation")
			}
			if mode == "declined" {
				if !strings.Contains(stderr.String(), "Delete this app") || !strings.Contains(stdout.String(), "Destroy canceled") {
					t.Fatal("destroy did not explain the confirmation or cancellation")
				}
			} else if !json.Valid(stdout.Bytes()) || stderr.Len() != 0 {
				t.Fatalf("destroy did not return one JSON envelope: %s %s", &stdout, &stderr)
			}
			if strings.Contains(stdout.String()+stderr.String(), "private-token") {
				t.Fatal("destroy leaked the management token")
			}
			if _, err := os.Stat(filepath.Join(directory, "zorveus.prod.destroy.plan.json")); err != nil {
				t.Fatal("destroy did not save its environment-specific plan")
			}
		})
	}
}
