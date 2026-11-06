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

func TestManagementReadsPreservePolicyAndDiscardSecretFields(t *testing.T) {
	connectionJSON := `{
		"app_connection_id":"aconn_a", "app_id":"app_a", "selected_org_id":"org_a",
		"connection_type":"org_programmatic", "status":"active",
		"credit_mode":"enforce", "overrun_policy":"limit_output", "allowed_models":["*"],
		"product_user_charge_multiplier":"1.000001", "effective_product_user_charge_multiplier":"1.000001",
		"active_key":{"api_key_id":"key_a", "app_connection_id":"aconn_a", "status":"active"},
		"api_key":"secret-that-must-not-be-returned", "token_hash":"secret-hash"
	}`
	routerJSON := `{
		"api_key_id":"key_router", "org_id":"org_a", "app_id":"app_a",
		"status":"active", "default_selector_alias":"backend",
		"members":[{"app_connection_id":"aconn_a", "selected_api_key_id":"key_a", "selector_alias":"backend", "is_default":true}],
		"api_key":"secret-that-must-not-be-returned"
	}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer test-access" {
			t.Error("management reads must use authenticated GET requests")
		}
		switch request.URL.Path {
		case "/apps/app_a":
			_, _ = writer.Write([]byte(`{"app":{"app_id":"app_a","owner_org_id":"org_a","app_name":"Assistant","credit_units_per_usd":"1000.000000000001","client_secret":"secret-that-must-not-be-returned"}}`))
		case "/app-connections":
			if request.URL.Query().Get("org_id") != "org_a" || request.URL.Query().Get("app_id") != "app_a" || request.URL.Query().Get("limit") != "10" {
				t.Error("connection list lost its app, organization, or limit")
			}
			if request.URL.Query().Get("connection_type") != "org_programmatic" {
				t.Error("connection list must use the existing programmatic-connection filter")
			}
			_, _ = fmt.Fprintf(writer, `{"app_connections":[%s]}`, connectionJSON)
		case "/app-connections/aconn_a":
			_, _ = fmt.Fprintf(writer, `{"app_connection":%s,"cap_rule":{"cap_rule_id":"cap_a","org_id":"org_a","target_type":"app_connection","target_id":"aconn_a","amount":"0.000000000001","currency":"USD","period":"daily","status":"active"}}`, connectionJSON)
		case "/app-connections/aconn_a/keys":
			_, _ = fmt.Fprintf(writer, `{"app_connection":%s,"api_keys":[{"api_key_id":"key_a","app_connection_id":"aconn_a","api_key":"secret-that-must-not-be-returned"}]}`, connectionJSON)
		case "/orgs/org_a/routing-keys":
			if request.URL.Query().Get("app_id") != "app_a" {
				t.Error("routing list lost its app bound")
			}
			_, _ = fmt.Fprintf(writer, `{"items":[%s]}`, routerJSON)
		case "/orgs/org_a/apps/app_a/routing-keys/key_router":
			_, _ = writer.Write([]byte(routerJSON))
		default:
			t.Errorf("unexpected management path: %s", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewClient()
	ctx := context.Background()
	app, err := client.GetApp(ctx, server.URL, "org_a", "app_a", "test-access")
	if err != nil || app.CreditUnitsPerUSD == nil || *app.CreditUnitsPerUSD != "1000.000000000001" {
		t.Fatalf("app policy lost its decimal value: %v", err)
	}
	connections, err := client.ListAppConnections(ctx, server.URL, "org_a", "app_a", "test-access", 10)
	if err != nil || len(connections) != 1 || len(connections[0].AllowedModels) != 1 || connections[0].AllowedModels[0] != "*" {
		t.Fatalf("connection identity or model policy was lost: %v", err)
	}
	detail, err := client.GetAppConnection(ctx, server.URL, "org_a", "app_a", "aconn_a", "test-access")
	if err != nil || detail.Cap == nil || detail.Cap.Amount != "0.000000000001" {
		t.Fatalf("connection cap lost its decimal value: %v", err)
	}
	if detail.Connection.ProductUserChargeMultiplier == nil || *detail.Connection.ProductUserChargeMultiplier != "1.000001" {
		t.Fatal("connection pricing override lost its decimal value")
	}
	keys, err := client.ListConnectionKeys(ctx, server.URL, "org_a", "app_a", "aconn_a", "test-access")
	if err != nil || len(keys.Keys) != 1 || keys.Keys[0].ID != "key_a" {
		t.Fatalf("connection key metadata was lost: %v", err)
	}
	routers, err := client.ListRoutingKeys(ctx, server.URL, "org_a", "app_a", "test-access")
	if err != nil || len(routers) != 1 || routers[0].KeyID != "key_router" {
		t.Fatalf("existing routing key ID was lost: %v", err)
	}
	router, err := client.GetRoutingKey(ctx, server.URL, "org_a", "app_a", "key_router", "test-access")
	if err != nil || len(router.Members) != 1 || router.Members[0].ConnectionID != "aconn_a" || !router.Members[0].IsDefault {
		t.Fatalf("routing selection was lost: %v", err)
	}
	for _, result := range []any{app, connections, detail, keys, routers, router} {
		encoded, err := json.Marshal(result)
		if err != nil || strings.Contains(string(encoded), "secret-") {
			t.Fatal("a read result retained secret fields")
		}
	}
}

func TestConnectionReadRejectsForeignResourcesAndCaps(t *testing.T) {
	cases := []struct {
		name   string
		change func(*AppConnectionDetail)
	}{
		{"organization", func(result *AppConnectionDetail) { result.Connection.Organization = "org_b" }},
		{"app", func(result *AppConnectionDetail) { result.Connection.AppID = "app_b" }},
		{"connection", func(result *AppConnectionDetail) { result.Connection.ID = "aconn_b" }},
		{"OAuth connection", func(result *AppConnectionDetail) { result.Connection.Type = "user_oauth" }},
		{"active key", func(result *AppConnectionDetail) {
			result.Connection.ActiveKey = &InferenceKey{ID: "key_b", ConnectionID: "aconn_b"}
		}},
		{"cap", func(result *AppConnectionDetail) { result.Cap.TargetID = "aconn_b" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := AppConnectionDetail{
				Connection: AppConnection{ID: "aconn_a", AppID: "app_a", Organization: "org_a", Type: "org_programmatic"},
				Cap:        &ConnectionCap{Organization: "org_a", TargetType: "app_connection", TargetID: "aconn_a"},
			}
			test.change(&result)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_ = json.NewEncoder(writer).Encode(result)
			}))
			defer server.Close()
			_, err := NewClient().GetAppConnection(context.Background(), server.URL, "org_a", "app_a", "aconn_a", "test-access")
			if err == nil {
				t.Fatal("a response outside the requested resource was accepted")
			}
		})
	}
}

func TestManagementReadsRejectPathInjectionBeforeSendingCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Error("invalid resource ID reached the HTTP server")
	}))
	defer server.Close()
	for _, appID := range []string{"", "../admin", "app_a?org_id=org_b", "app_a/other"} {
		_, err := NewClient().GetApp(context.Background(), server.URL, "org_a", appID, "test-access")
		if err == nil {
			t.Fatal("invalid resource ID was accepted")
		}
	}
}
