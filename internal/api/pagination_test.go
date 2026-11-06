package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestConnectionPaginationKeepsFiltersAndReadsPastFullPages(t *testing.T) {
	for _, total := range []int{0, 1, 2, 3, 4} {
		t.Run(strconv.Itoa(total), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				query := request.URL.Query()
				offset, err := strconv.Atoi(query.Get("offset"))
				if err != nil || offset != requests*2 {
					t.Errorf("incorrect page offset %q", query.Get("offset"))
				}
				requests++
				if query.Get("limit") != "2" || query.Get("org_id") != "org_a" || query.Get("app_id") != "app_a" || query.Get("connection_type") != "org_programmatic" || query.Get("sort_by") != "created_at" || query.Get("sort_order") != "asc" {
					t.Error("connection filters changed while paging")
				}
				page := []AppConnection{}
				for index := offset; index < min(offset+2, total); index++ {
					page = append(page, AppConnection{ID: fmt.Sprintf("connection_%d", index), AppID: "app_a", Organization: "org_a", Type: "org_programmatic"})
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"app_connections": page})
			}))
			defer server.Close()
			connections, err := NewClient().ListAppConnections(context.Background(), server.URL, "org_a", "app_a", "token", 2)
			if err != nil || len(connections) != total || requests != total/2+1 {
				t.Fatalf("incomplete connection listing: records=%d requests=%d err=%v", len(connections), requests, err)
			}
		})
	}
}

func TestProviderPaginationReadsShortAndExactlyFullCollections(t *testing.T) {
	for _, total := range []int{0, 999, 1000, 1001, 2000} {
		t.Run(strconv.Itoa(total), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				query := request.URL.Query()
				offset, err := strconv.Atoi(query.Get("offset"))
				if err != nil || offset != requests*1000 || query.Get("limit") != "1000" || query.Get("org_id") != "org_a" {
					t.Error("provider page query changed")
				}
				requests++
				page := []ProviderCredential{}
				for index := offset; index < min(offset+1000, total); index++ {
					page = append(page, ProviderCredential{ID: fmt.Sprintf("provider_%d", index), Organization: "org_a"})
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"provider_credentials": page})
			}))
			defer server.Close()
			credentials, err := NewClient().ListProviderCredentials(context.Background(), server.URL, "org_a", "token")
			if err != nil || len(credentials) != total || requests != total/1000+1 {
				t.Fatalf("incomplete provider listing: records=%d requests=%d err=%v", len(credentials), requests, err)
			}
		})
	}
}

func TestPaginationRejectsRepeatedOrForeignRecordsWithoutPartialResults(t *testing.T) {
	for _, scenario := range []string{"repeated", "foreign"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				organization := "org_a"
				if scenario == "foreign" && request.URL.Query().Get("offset") == "1" {
					organization = "org_other"
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"app_connections": []AppConnection{{ID: "connection_a", AppID: "app_a", Organization: organization, Type: "org_programmatic"}}})
			}))
			defer server.Close()
			connections, err := NewClient().ListAppConnections(context.Background(), server.URL, "org_a", "app_a", "token", 1)
			expected := "collection_changed"
			if scenario == "foreign" {
				expected = "resource_mismatch"
			}
			if err == nil || problem.Safe(err).Code != expected || connections != nil {
				t.Fatalf("unsafe paginated result: %v %v", connections, err)
			}
		})
	}
}
