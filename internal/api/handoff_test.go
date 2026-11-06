package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMeUsesExistingResponseAndChecksDelegatedOrganization(t *testing.T) {
	for _, organization := range []string{"org_a", "org_other"} {
		t.Run(organization, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.URL.Path != "/me" || request.Header.Get("Authorization") != "Bearer test-access" {
					t.Error("session read must use the existing /me endpoint and bearer header")
				}
				_, _ = fmt.Fprintf(writer, `{
					"user":{"user_id":"usr_a","email":"person@example.test","first_name":"Person","last_name":null,"token_hash":"private-hash"},
					"default_org_id":"org_default",
					"orgs":[{"org_id":%q,"org_name":"Selected workspace","role":"owner"}],
					"session":{"authenticated":true,"expires_at":"2030-01-01T00:00:00Z","access_token":"private-access"}
				}`, organization)
			}))
			defer server.Close()
			me, err := NewClient().GetMe(context.Background(), server.URL, "org_a", "test-access")
			if organization != "org_a" {
				if err == nil {
					t.Fatal("another organization's session response was accepted")
				}
				return
			}
			if err != nil || me.User.ID != "usr_a" || !me.Session.Authenticated {
				t.Fatalf("existing user/session response was not read: %v", err)
			}
			encoded, err := json.Marshal(me)
			if err != nil || strings.Contains(string(encoded), "private-") {
				t.Fatal("session DTO retained unapproved credential fields")
			}
		})
	}
}

func TestMeRejectsIncompleteOrUnauthenticatedResponses(t *testing.T) {
	responses := []string{
		`{"user":{"user_id":"usr_a","email":"person@example.test"},"session":{"authenticated":false,"expires_at":"2030-01-01T00:00:00Z"}}`,
		`{"user":{"user_id":"usr_a","email":"person@example.test"},"session":{"authenticated":true}}`,
		`{"user":{"email":"person@example.test"},"session":{"authenticated":true,"expires_at":"2030-01-01T00:00:00Z"}}`,
	}
	for _, body := range responses {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = writer.Write([]byte(body))
		}))
		_, err := NewClient().GetMe(context.Background(), server.URL, "org_a", "test-access")
		server.Close()
		if err == nil {
			t.Fatal("an incomplete or unauthenticated session was accepted")
		}
	}
}

func TestRoutingRenameUsesExistingIDAndOnlyChangesDisplayName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPatch || request.URL.Path != "/orgs/org_a/apps/app_a/routing-keys/key_router" {
			t.Error("rename must patch the existing routing key")
		}
		if request.Header.Get("Authorization") != "Bearer test-access" || request.Header.Get("Content-Type") != "application/json" {
			t.Error("rename lost authentication or JSON content type")
		}
		var payload map[string]string
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || len(payload) != 1 || payload["key_alias"] != "Updated router" {
			t.Error("rename must send only key_alias")
		}
		_, _ = writer.Write([]byte(`{
			"api_key_id":"key_router","app_id":"app_a","org_id":"org_a","key_alias":"Updated router",
			"status":"active","members":[{"app_connection_id":"aconn_a","selected_api_key_id":"key_a","selector_alias":"free","is_default":true}],
			"default_selector_alias":"free","api_key":"private-secret"
		}`))
	}))
	defer server.Close()
	key, err := NewClient().RenameRoutingKey(context.Background(), server.URL, "org_a", "app_a", "key_router", "Updated router", "test-access")
	if err != nil || key.KeyID != "key_router" || len(key.Members) != 1 || key.Members[0].KeyID != "key_a" {
		t.Fatalf("rename lost the existing key or memberships: %v", err)
	}
	if key.DefaultSelector == nil || *key.DefaultSelector != "free" {
		t.Fatal("rename lost the default selector")
	}
	encoded, err := json.Marshal(key)
	if err != nil || strings.Contains(string(encoded), "private-secret") {
		t.Fatal("rename result retained a raw credential")
	}
}

func TestRoutingRenameRejectsReplacementCredentialID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"api_key_id":"key_replacement","app_id":"app_a","org_id":"org_a","key_alias":"Updated router"}`))
	}))
	defer server.Close()
	_, err := NewClient().RenameRoutingKey(context.Background(), server.URL, "org_a", "app_a", "key_router", "Updated router", "test-access")
	if err == nil {
		t.Fatal("rename accepted a replacement credential instead of the original key")
	}
}
