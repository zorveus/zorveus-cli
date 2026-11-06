package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCanonicalOriginEnforcesTransportAndRejectsEmbeddedCredentials(t *testing.T) {
	cases := []struct {
		address string
		local   bool
		want    string
	}{
		{"https://API.example.test:443/", false, "https://api.example.test"},
		{"https://[::1]:443", false, "https://[::1]"},
		{"http://127.0.0.1:8000", true, "http://127.0.0.1:8000"},
		{"http://[::1]:8000", true, "http://[::1]:8000"},
		{"http://127.0.0.1:8000", false, ""},
		{"http://localhost:8000", true, "http://localhost:8000"},
		{"http://localhost.evil.test:8000", true, ""},
		{"http://example.test", true, ""},
		{"https://name:secret@example.test", false, ""},
		{"https://example.test/path", false, ""},
		{"https://example.test?token=secret", false, ""},
		{"https://example.test/#fragment", false, ""},
	}
	for _, test := range cases {
		t.Run(test.address, func(t *testing.T) {
			origin, err := CanonicalOrigin(test.address, test.local)
			if test.want == "" {
				if err == nil {
					t.Fatal("unsafe origin was accepted")
				}
				if strings.Contains(err.Error(), "secret") {
					t.Fatal("origin error exposed user input")
				}
				return
			}
			if err != nil || origin != test.want {
				t.Fatalf("origin = %q, error = %v", origin, err)
			}
		})
	}
}

func TestRequestsNeverFollowRedirects(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirected.Add(1)
	}))
	defer destination.Close()
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, destination.URL, status)
		}))
		client := NewClient()
		_, readError := client.ListApps(context.Background(), server.URL, "org_a", "private-access", 100)
		_, tokenError := client.Refresh(context.Background(), server.URL, "private-refresh")
		revokeError := client.Revoke(context.Background(), server.URL, "private-refresh")
		server.Close()
		if readError == nil || tokenError == nil || revokeError == nil {
			t.Fatal("redirect was accepted")
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("a credential-bearing request followed a redirect")
	}
}

func TestAppListRejectsAnotherOrganization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("owner_org_id") != "org_a" {
			t.Error("organization was not sent")
		}
		_, _ = writer.Write([]byte(`{"apps":[{"app_id":"app_b","owner_org_id":"org_b"}]}`))
	}))
	defer server.Close()
	_, err := NewClient().ListApps(context.Background(), server.URL, "org_a", "private-access", 100)
	if err == nil {
		t.Fatal("another organization's app was returned")
	}
}

func TestAPIErrorNeverEchoesResponseSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":"private-refresh","detail":"private-access"}`))
	}))
	defer server.Close()
	_, err := NewClient().Refresh(context.Background(), server.URL, "private-refresh")
	if err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatalf("unsafe API error: %v", err)
	}
}
