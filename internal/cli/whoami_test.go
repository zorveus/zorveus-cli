package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
)

func TestWhoamiJSONUsesVerifiedMeAndExcludesCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/me" || request.Header.Get("Authorization") != "Bearer private-access" {
			t.Error("whoami must use authenticated /me")
		}
		_, _ = fmt.Fprint(writer, `{
			"user":{"user_id":"usr_a","email":"person@example.test","first_name":"Person","token_hash":"private-hash"},
			"default_org_id":"org_other","orgs":[{"org_id":"org_a"}],
			"session":{"authenticated":true,"expires_at":"2030-01-01T00:00:00Z","access_token":"private-server-token"}
		}`)
	}))
	defer server.Close()
	store := &testStore{present: true, login: auth.Login{
		APIOrigin: server.URL, ClientID: api.ClientID, Organization: "org_a",
		AccessToken: "private-access", RefreshToken: "private-refresh",
		ExpiresAt: time.Now().Add(time.Minute), Scope: "routing_keys:read",
	}}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"whoami", "--json", "--non-interactive"}, &stdout, &stderr, testDependencies(store))
	if code != 0 {
		t.Fatalf("whoami failed: %s", stderr.String())
	}
	var envelope struct {
		OK      bool              `json:"ok"`
		Command string            `json:"command"`
		Result  auth.WhoamiResult `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || envelope.Command != "zorveus whoami" || envelope.Result.User.ID != "usr_a" || envelope.Result.Organization != "org_a" {
		t.Fatal("whoami lost verified identity or selected organization")
	}
	if strings.Contains(stdout.String()+stderr.String(), "private-") {
		t.Fatal("whoami exposed credentials or unapproved server fields")
	}
}
