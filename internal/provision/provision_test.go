package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

const fullScope = "apps:read apps:create apps:update apps:rotate_secret app_connections:read app_connections:write app_connections:revoke routing_keys:read routing_keys:write caps:app_write caps:org_write service_keys:read service_keys:write service_keys:revoke provider_credentials:read provider_credentials:write"

const newManifest = `{"version":1,"app":{"name":"Demo","allowedModels":["*"]},"keys":[{"name":"Primary","routingAlias":"primary","cap":{"amount":"100.000000000001","currency":"USD","period":"monthly"}},{"name":"Backup","routingAlias":"backup"}],"routingKeys":[{"name":"Router","members":["primary","backup"],"defaultSelector":"primary"}]}`

type loginStore struct{ login auth.Login }

func (store *loginStore) Load() (auth.Login, error)   { return store.login, nil }
func (store *loginStore) Save(login auth.Login) error { store.login = login; return nil }
func (store *loginStore) Delete() error               { return nil }

type memorySecrets struct {
	values            map[string]Credential
	failSave          bool
	failAccountPrefix string
}

func (store *memorySecrets) Check(string) error { return nil }
func (store *memorySecrets) Load(account string) (Credential, error) {
	value, ok := store.values[account]
	if !ok {
		return Credential{}, ErrSecretMissing
	}
	return value, nil
}
func (store *memorySecrets) Save(account string, value Credential) error {
	if store.failSave || (store.failAccountPrefix != "" && strings.HasPrefix(account, store.failAccountPrefix)) {
		return secretStoreError()
	}
	store.values[account] = value
	return nil
}

// The transport uses real HTTP requests and response decoding without opening
// a listener or touching a live workspace.
type fakeBackend struct {
	t                           *testing.T
	app                         Fields
	connections                 map[string]Fields
	caps                        map[string]Fields
	routers                     map[string]Fields
	writes                      []string
	loseCreateResponse          bool
	changeConfig                string
	services                    map[string]Fields
	providers                   map[string]Fields
	providerSecret              string
	providerVersions            int
	reads                       []string
	serviceRotations            int
	loseServiceRotationResponse bool
	denyServiceRotation         bool
	capReadDelay                int
	pendingCapReads             map[string]int
	capReadBackAmount           string
}

func (backend *fakeBackend) RoundTrip(request *http.Request) (*http.Response, error) {
	writer := httptest.NewRecorder()
	backend.serve(writer, request)
	return writer.Result(), nil
}

func (backend *fakeBackend) serve(writer http.ResponseWriter, request *http.Request) {
	backend.t.Helper()
	if request.Header.Get("Authorization") != "Bearer private-management-token" {
		backend.t.Fatal("missing management authorization")
	}
	path := request.URL.Path
	if strings.HasSuffix(path, "/revoke") && request.Body != nil {
		body, err := io.ReadAll(request.Body)
		if err != nil || len(body) != 0 {
			backend.t.Fatal("revocation requires an empty body; JSON null is rejected")
		}
	}
	if request.Method == http.MethodGet {
		backend.reads = append(backend.reads, path)
		switch {
		case path == "/me":
			_ = json.NewEncoder(writer).Encode(map[string]any{"user": map[string]string{"user_id": "user_a", "email": "user@example.test"}, "session": map[string]any{"authenticated": true, "expires_at": time.Now().Add(time.Hour)}, "orgs": []map[string]string{{"org_id": "org_a"}}})
		case path == "/apps/app_a":
			if backend.app == nil {
				http.NotFound(writer, request)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"app": backend.app})
		case path == "/app-connections":
			if request.URL.Query().Get("org_id") != "org_a" || request.URL.Query().Get("app_id") != "app_a" || request.URL.Query().Get("connection_type") != "org_programmatic" {
				backend.t.Fatal("incorrect connection list target")
			}
			connections := []Fields{}
			for _, connection := range backend.connections {
				connections = append(connections, connection)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"app_connections": connections})
		case path == "/orgs/org_a/routing-keys":
			if request.URL.Query().Get("app_id") != "app_a" {
				backend.t.Fatal("incorrect routing list target")
			}
			routers := []Fields{}
			for _, router := range backend.routers {
				routers = append(routers, router)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"items": routers})
		case path == "/orgs/org_a/service-keys":
			keys := []Fields{}
			for _, key := range backend.services {
				keys = append(keys, key)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"service_keys": keys})
		case path == "/provider-credentials":
			if request.URL.Query().Get("org_id") != "org_a" {
				backend.t.Fatal("missing provider organization")
			}
			credentials := []Fields{}
			for _, credential := range backend.providers {
				credentials = append(credentials, credential)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"provider_credentials": credentials})
		case strings.HasPrefix(path, "/app-connections/"):
			if request.URL.Query().Get("org_id") != "org_a" {
				backend.t.Fatal("missing connection organization")
			}
			id := strings.Split(path, "/")[2]
			connection := backend.connections[id]
			if connection == nil {
				http.NotFound(writer, request)
				return
			}
			if strings.HasSuffix(path, "/keys") {
				_ = json.NewEncoder(writer).Encode(map[string]any{"app_connection": connection, "api_keys": []any{connection["active_key"]}})
			} else {
				cap := backend.caps[id]
				if backend.pendingCapReads[id] > 0 {
					backend.pendingCapReads[id]--
					cap = nil
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"app_connection": connection, "cap_rule": cap})
			}
		case strings.Contains(path, "/routing-keys/"):
			id := strings.Split(path, "/")[6]
			if backend.routers[id] == nil {
				http.NotFound(writer, request)
				return
			}
			_ = json.NewEncoder(writer).Encode(backend.routers[id])
		default:
			backend.t.Fatalf("unexpected read %s", path)
		}
		return
	}
	backend.writes = append(backend.writes, request.Method+" "+path)
	var payload Fields
	if request.Body != nil {
		_ = json.NewDecoder(request.Body).Decode(&payload)
	}
	response := any(map[string]any{})
	switch {
	case path == "/apps" && request.Method == http.MethodPost:
		if payload.text("owner_org_id") != "org_a" {
			backend.t.Fatal("incorrect app owner")
		}
		backend.app = payload
		backend.app.set("app_id", "app_a")
		backend.app.set("status", "active")
		if !backend.app.has("allowed_scopes") {
			backend.app.set("allowed_scopes", []string{"inference:read", "inference:write"})
		}
		if !backend.app.has("product_user_charge_multiplier") {
			backend.app.set("product_user_charge_multiplier", "1")
		}
		uris := backend.app.strings("redirect_uris")
		redirects := []Fields{}
		for index, uri := range uris {
			redirects = append(redirects, fields(map[string]string{"redirect_uri_id": fmt.Sprintf("redirect_%d", index+1), "redirect_uri": uri, "status": "active"}))
		}
		backend.app.set("redirect_uris", redirects)
		response = map[string]any{"app": backend.app, "client_secret": "private-app-secret"}
		if backend.app.text("client_type") == "public" {
			response = map[string]any{"app": backend.app, "client_secret": nil}
		}
	case path == "/apps/app_a/client-secret/rotate":
		response = map[string]any{"app_id": "app_a", "client_secret": "private-rotated-app-secret"}
	case path == "/apps/app_a" || path == "/apps/app_a/models":
		for name, value := range payload {
			backend.app[name] = value
		}
	case strings.HasPrefix(path, "/apps/app_a/redirect-uris"):
		var redirects []Fields
		_ = json.Unmarshal(backend.app["redirect_uris"], &redirects)
		if request.Method == http.MethodPost {
			redirects = append(redirects, fields(map[string]string{"redirect_uri_id": fmt.Sprintf("redirect_%d", len(redirects)+1), "redirect_uri": payload.text("redirect_uri"), "status": "active"}))
		} else {
			for _, redirect := range redirects {
				if strings.HasSuffix(path, "/"+redirect.text("redirect_uri_id")) {
					redirect.set("status", "disabled")
				}
			}
		}
		backend.app.set("redirect_uris", redirects)
	case path == "/orgs/org_a/service-keys":
		if backend.services == nil {
			backend.services = map[string]Fields{}
		}
		id := fmt.Sprintf("service_%d", len(backend.services)+1)
		key := payload
		key.set("org_service_api_key_id", id)
		key.set("org_id", "org_a")
		key.set("status", "active")
		key.set("token_prefix", "service-prefix-original")
		backend.services[id] = key
		response = fields(key)
		response.(Fields).set("service_key", "private-service-secret")
	case strings.HasPrefix(path, "/orgs/org_a/service-keys/"):
		id := strings.Split(path, "/")[4]
		key := backend.services[id]
		switch {
		case request.Method == http.MethodPatch:
			for name, value := range payload {
				if name != "key_alias" && name != "scopes" {
					backend.t.Fatal("unexpected service-key update field")
				}
				key[name] = value
			}
			response = key
		case strings.HasSuffix(path, "/rotate"):
			if request.ContentLength != 0 {
				backend.t.Fatal("service rotation must send no body")
			}
			if backend.denyServiceRotation {
				writer.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(writer, `{}`)
				return
			}
			backend.serviceRotations++
			key.set("token_prefix", fmt.Sprintf("service-prefix-%d", backend.serviceRotations))
			response = fields(key)
			response.(Fields).set("service_key", "private-rotated-service-secret")
			if backend.loseServiceRotationResponse {
				_, _ = io.WriteString(writer, "{")
				return
			}
		default:
			key.set("status", "revoked")
			response = map[string]any{"service_key": key, "revoked": true}
		}
	case path == "/provider-credentials":
		if backend.providers == nil {
			backend.providers = map[string]Fields{}
		}
		backend.providerSecret = payload.text("secret")
		delete(payload, "secret")
		id := fmt.Sprintf("provider_%d", len(backend.providers)+1)
		backend.providerVersions++
		payload.set("provider_credential_id", id)
		payload.set("org_id", "org_a")
		payload.set("status", "active")
		payload.set("active_secret_version_id", fmt.Sprintf("version_%d", backend.providerVersions))
		backend.providers[id] = payload
		response = payload
	case strings.HasPrefix(path, "/provider-credentials/") && strings.HasSuffix(path, "/rotate"):
		backend.providerSecret = payload.text("secret")
		id := strings.Split(path, "/")[2]
		backend.providerVersions++
		backend.providers[id].set("active_secret_version_id", fmt.Sprintf("version_%d", backend.providerVersions))
		response = map[string]any{"provider_credential": backend.providers[id], "rotated": true}
	case strings.HasPrefix(path, "/provider-credentials/") && request.Method == http.MethodPatch:
		if request.URL.Query().Get("org_id") != "org_a" {
			backend.t.Fatal("missing provider update organization")
		}
		id := strings.Split(path, "/")[2]
		credential := backend.providers[id]
		for name, value := range payload {
			if name != "credential_name" && name != "routing_mode" && name != "default_model_policy" && name != "provider_config" {
				backend.t.Fatal("unexpected provider update field")
			}
			credential[name] = value
		}
		response = credential
	case path == "/app-connections/dashboard":
		if payload.text("org_id") != "org_a" || payload.text("app_id") != "app_a" {
			backend.t.Fatal("incorrect connection target")
		}
		id := fmt.Sprintf("conn_%d", len(backend.connections)+1)
		keyID := "key_" + id
		connection := fields(map[string]any{"app_connection_id": id, "app_id": "app_a", "selected_org_id": "org_a", "connection_type": "org_programmatic", "status": "active", "credit_mode": payload.text("credit_mode"), "overrun_policy": payload.text("overrun_policy"), "approved_scopes": payload.strings("approved_scopes"), "allowed_models": payload.strings("allowed_models")})
		credential := fields(map[string]any{"api_key_id": keyID, "app_connection_id": id, "key_alias": payload.text("key_alias"), "allowed_models": payload.strings("allowed_models"), "status": "active"})
		connection.set("active_key", credential)
		backend.connections[id] = connection
		response = map[string]any{"app_connection": connection, "api_key_id": keyID, "api_key": "private-inference-secret"}
	case strings.HasPrefix(path, "/app-connections/"):
		if request.URL.Query().Get("org_id") != "org_a" {
			backend.t.Fatal("missing write organization")
		}
		id := strings.Split(path, "/")[2]
		connection := backend.connections[id]
		switch {
		case strings.HasSuffix(path, "/keys/rotate"):
			var credential Fields
			_ = json.Unmarshal(connection["active_key"], &credential)
			credential.set("api_key_id", "rotated_"+id)
			connection.set("active_key", credential)
			response = map[string]any{"app_connection": connection, "new_api_key_id": "rotated_" + id, "api_key": "private-rotated-inference-secret"}
		case strings.Contains(path, "/keys/"):
			var credential Fields
			_ = json.Unmarshal(connection["active_key"], &credential)
			for name, value := range payload {
				credential[name] = value
			}
			connection.set("active_key", credential)
		case strings.HasSuffix(path, "/cap"):
			cap := payload
			cap.set("cap_rule_id", "cap_"+id)
			cap.set("org_id", "org_a")
			cap.set("target_type", "app_connection")
			cap.set("target_id", id)
			cap.set("status", "active")
			backend.caps[id] = cap
			if backend.capReadBackAmount != "" {
				cap.set("amount", backend.capReadBackAmount)
			}
			if backend.capReadDelay > 0 {
				if backend.pendingCapReads == nil {
					backend.pendingCapReads = map[string]int{}
				}
				backend.pendingCapReads[id] = backend.capReadDelay
			}
		case strings.HasSuffix(path, "/revoke"):
			connection.set("status", "revoked")
		default:
			for name, value := range payload {
				connection[name] = value
			}
		}
	case strings.HasPrefix(path, "/caps/"):
		for id, cap := range backend.caps {
			if cap.text("cap_rule_id") == strings.TrimPrefix(path, "/caps/") {
				delete(backend.caps, id)
			}
		}
	case path == "/orgs/org_a/apps/app_a/routing-keys":
		id := fmt.Sprintf("router_%d", len(backend.routers)+1)
		router := fields(map[string]any{"api_key_id": id, "app_id": "app_a", "org_id": "org_a", "status": "active", "key_alias": payload.text("key_alias")})
		backend.setMembers(router, payload)
		backend.routers[id] = router
		response = map[string]any{"routing_key": router, "api_key": "private-routing-secret"}
	case strings.Contains(path, "/routing-keys/"):
		id := strings.Split(path, "/")[6]
		router := backend.routers[id]
		if strings.HasSuffix(path, "/members") {
			backend.setMembers(router, payload)
		} else if strings.HasSuffix(path, "/revoke") {
			router.set("status", "revoked")
		} else if strings.HasSuffix(path, "/rotate") {
			router.set("status", "revoked")
			replacement := fields(router)
			replacement.set("status", "active")
			replacement.set("api_key_id", "rotated_"+id)
			backend.routers["rotated_"+id] = replacement
			response = map[string]any{"routing_key": replacement, "api_key": "private-rotated-routing-secret"}
		} else {
			router["key_alias"] = payload["key_alias"]
		}
	default:
		backend.t.Fatalf("unexpected write %s %s", request.Method, path)
	}
	if backend.changeConfig != "" {
		_ = os.WriteFile(backend.changeConfig, []byte(`{"version":1,"app":{"name":"User edit","allowedModels":["*"]}}`), 0600)
		backend.changeConfig = ""
	}
	if backend.loseCreateResponse && path == "/apps" {
		_, _ = io.WriteString(writer, "{")
		return
	}
	_ = json.NewEncoder(writer).Encode(response)
}

func (backend *fakeBackend) setMembers(router, payload Fields) {
	var members []Fields
	_ = json.Unmarshal(payload["members"], &members)
	for _, member := range members {
		matched := false
		for id, connection := range backend.connections {
			var credential Fields
			_ = json.Unmarshal(connection["active_key"], &credential)
			if credential.text("api_key_id") == member.text("selected_api_key_id") {
				member.set("app_connection_id", id)
				member.set("status", "active")
				matched = true
				break
			}
		}
		if !matched {
			backend.t.Fatal("routing alias did not resolve to a current inference key ID")
		}
	}
	router.set("members", members)
	router["default_selector_alias"] = payload["default_selector_alias"]
}

func setup(t *testing.T) (*Service, *fakeBackend, *loginStore, string, string) {
	t.Helper()
	backend := &fakeBackend{t: t, connections: map[string]Fields{}, caps: map[string]Fields{}, routers: map[string]Fields{}}
	store := &loginStore{auth.Login{APIOrigin: "https://api.example.test", ClientID: api.ClientID, Organization: "org_a", AccessToken: "private-management-token", ExpiresAt: time.Now().Add(time.Hour), Scope: fullScope}}
	service := &Service{Auth: &auth.Service{API: &api.Client{HTTP: &http.Client{Transport: backend}}, Store: store, Now: time.Now, Lock: func(context.Context) (func(), error) { return func() {}, nil }}, Secrets: &memorySecrets{values: map[string]Credential{}}}
	directory := t.TempDir()
	config := filepath.Join(directory, "zorveus.json")
	if err := os.WriteFile(config, []byte(newManifest), 0600); err != nil {
		t.Fatal(err)
	}
	return service, backend, store, config, filepath.Join(directory, "plan.json")
}

func planAndApply(t *testing.T, service *Service, config, path string) {
	t.Helper()
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatal(err)
	}
}

func TestCreateWritesIDsResolvesAliasesAndConverges(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	expected := []string{"POST /apps", "POST /app-connections/dashboard", "PATCH /app-connections/conn_1/cap", "POST /app-connections/dashboard", "POST /orgs/org_a/apps/app_a/routing-keys"}
	if !slices.Equal(backend.writes, expected) {
		t.Fatalf("creation order: %v", backend.writes)
	}
	desired, _, _, err := loadManifest(config)
	if err != nil {
		t.Fatal(err)
	}
	if desired.App.text("id") != "app_a" || desired.Keys[0].text("id") != "conn_1" || desired.Routers[0].text("id") != "router_1" {
		t.Fatal("returned IDs not recorded")
	}
	if backend.caps["conn_1"].text("amount") != "100.000000000001" {
		t.Fatal("decimal cap lost precision")
	}
	if len(service.Secrets.(*memorySecrets).values) != 4 {
		t.Fatal("one-time credentials not delivered")
	}
	if _, err := service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatal(err)
	}
	if len(backend.writes) != len(expected) {
		t.Fatal("completed apply repeated writes")
	}
	result, err := service.Plan(context.Background(), "", config, path+".next")
	if err != nil {
		t.Fatal(err)
	}
	if result.Operations != 0 {
		t.Fatalf("converged plan contains changes: %+v", result.Changes)
	}
	_ = filepath.WalkDir(filepath.Dir(config), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "private-") {
			t.Errorf("secret leaked to %s", path)
		}
		return nil
	})
}

func TestLostCreationResponseNeverRepeatsCreate(t *testing.T) {
	service, backend, _, config, path := setup(t)
	backend.loseCreateResponse = true
	_, err := service.Plan(context.Background(), "", config, path)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, err = service.Apply(context.Background(), "", path, true, false)
		if err == nil || problem.Safe(err).ExitCode != 5 {
			t.Fatalf("expected uncertain outcome, got %v", err)
		}
	}
	if len(backend.writes) != 1 {
		t.Fatal("unknown creation repeated")
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{path + ".new", path} {
		if _, err := service.Plan(context.Background(), "", config, output); err == nil || problem.Safe(err).Code != "apply_pending" {
			t.Fatalf("replanning bypassed pending creation: %v", err)
		}
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != string(original) {
		t.Fatal("replanning replaced the plan needed to resume an unfinished apply")
	}
}

func TestCredentialStorageFailureResumesWithRotation(t *testing.T) {
	service, backend, _, config, path := setup(t)
	secrets := service.Secrets.(*memorySecrets)
	secrets.failSave = true
	_, err := service.Plan(context.Background(), "", config, path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Apply(context.Background(), "", path, true, false)
	if err == nil || problem.Safe(err).ExitCode != 5 {
		t.Fatalf("expected partial apply, got %v", err)
	}
	secrets.failSave = false
	if _, err = service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatal(err)
	}
	if backend.writes[1] != "POST /apps/app_a/client-secret/rotate" {
		t.Fatalf("did not recover through rotation: %v", backend.writes)
	}
	if slices.Contains(backend.writes[1:], "POST /apps") {
		t.Fatal("app recreated instead of rotated")
	}
}

func TestPreflightRejectsMissingScopesAndStalePlans(t *testing.T) {
	for _, change := range []string{"scope", "manifest", "remote", "tamper", "saved_manifest", "target"} {
		t.Run(change, func(t *testing.T) {
			service, backend, store, config, path := setup(t)
			planAndApply(t, service, config, path)
			data, _ := os.ReadFile(config)
			data = []byte(strings.Replace(string(data), `"Demo"`, `"Renamed"`, 1))
			_ = os.WriteFile(config, data, 0600)
			path += ".update"
			if _, err := service.Plan(context.Background(), "", config, path); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "scope":
				store.login.Scope = "apps:read app_connections:read routing_keys:read"
			case "manifest":
				_ = os.WriteFile(config, append(data, ' '), 0600)
			case "remote":
				backend.app.set("app_name", "Another edit")
			case "target":
				store.login.Organization = "org_other"
			case "tamper":
				var plan Plan
				_, _, _ = loadJSON(path, &plan)
				plan.Steps[0].After.set("app_name", "Tampered")
				_ = os.WriteFile(path, encoded(plan), 0600)
			case "saved_manifest":
				var plan Plan
				_, _, _ = loadJSON(path, &plan)
				plan.Manifest = json.RawMessage(strings.Replace(string(plan.Manifest), "Renamed", "Tampered", 1))
				_ = os.WriteFile(path, encoded(plan), 0600)
			}
			before := len(backend.writes)
			if _, err := service.Apply(context.Background(), "", path, true, false); err == nil {
				t.Fatal("preflight accepted changed assumptions")
			}
			if len(backend.writes) != before {
				t.Fatal("write happened before preflight rejection")
			}
		})
	}
}

func TestUpdatesKeepResourceIDsAndDoNotRotateCredentials(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	desired, _, _, err := loadManifest(config)
	if err != nil {
		t.Fatal(err)
	}
	desired.App.set("name", "Updated app")
	desired.Keys[0].set("name", "Updated key")
	desired.Keys[0].set("creditMode", "track_only")
	desired.Keys[0].set("overrunPolicy", "allow_overrun")
	desired.Keys[0].set("productUserChargeMultiplier", "1.500000")
	desired.Keys[0].set("cap", nil)
	desired.Routers[0].set("name", "Updated router")
	desired.Routers[0].set("members", []string{"backup"})
	desired.Routers[0].set("defaultSelector", nil)
	document := fields(map[string]any{"version": 1, "app": desired.App, "keys": desired.Keys, "routingKeys": desired.Routers})
	if err := os.WriteFile(config, encoded(document), 0600); err != nil {
		t.Fatal(err)
	}
	path += ".update"
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	before := len(backend.writes)
	if _, err := service.Apply(context.Background(), "", path, true, true); err != nil {
		t.Fatal(err)
	}
	for _, write := range backend.writes[before:] {
		if strings.Contains(write, "/rotate") || strings.HasPrefix(write, "POST ") {
			t.Fatalf("update recreated or rotated a credential: %s", write)
		}
	}
	if backend.routers["router_1"].text("key_alias") != "Updated router" || backend.caps["conn_1"] != nil {
		t.Fatal("rename or cap removal not applied")
	}
	result, err := service.Plan(context.Background(), "", config, path+".next")
	if err != nil || result.Operations != 0 {
		t.Fatalf("updates did not converge: %v %+v", err, result.Changes)
	}
}

func TestKnownInferenceSecretRecoversWithCurrentCredentialID(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	var plan Plan
	_, _, _ = loadJSON(path, &plan)
	var journal Journal
	_, _, _ = loadJSON(journalPath(plan), &journal)
	// Simulate interruption after receiving the key's ID but before saving
	// its secret. The source and state still belong to the original plan.
	journal.Complete = false
	journal.FinalDigest = ""
	journal.Steps[1].Done = false
	delete(service.Secrets.(*memorySecrets).values, plan.ID+"/key:primary")
	if err := os.WriteFile(config, []byte(newManifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statePath(config)); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(journalPath(plan), encoded(journal)); err != nil {
		t.Fatal(err)
	}
	before := len(backend.writes)
	if _, err := service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(backend.writes[before:], []string{"POST /app-connections/conn_1/keys/rotate"}) {
		t.Fatalf("unexpected recovery writes: %v", backend.writes[before:])
	}
	credential := service.Secrets.(*memorySecrets).values[plan.ID+"/key:primary"]
	if credential.ResourceID != "conn_1" || credential.CredentialID != "rotated_conn_1" {
		t.Fatal("replacement credential ID not recorded")
	}
}

func TestManifestEditedDuringApplyIsPreserved(t *testing.T) {
	service, backend, _, config, path := setup(t)
	_, err := service.Plan(context.Background(), "", config, path)
	if err != nil {
		t.Fatal(err)
	}
	backend.changeConfig = config
	_, err = service.Apply(context.Background(), "", path, true, false)
	if err == nil || problem.Safe(err).Code != "config_changed" {
		t.Fatalf("expected config conflict: %v", err)
	}
	data, _ := os.ReadFile(config)
	if !strings.Contains(string(data), "User edit") {
		t.Fatal("user edits overwritten")
	}
	before := len(backend.writes)
	_, _ = service.Apply(context.Background(), "", path, true, false)
	if len(backend.writes) != before {
		t.Fatal("changed source caused new writes on resume")
	}
}

func TestRoutingSecretRecoveryRecordsReplacementID(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	var plan Plan
	_, _, _ = loadJSON(path, &plan)
	var journal Journal
	_, _, _ = loadJSON(journalPath(plan), &journal)
	journal.Complete, journal.FinalDigest = false, ""
	journal.Steps[len(journal.Steps)-1].Done = false
	delete(service.Secrets.(*memorySecrets).values, plan.ID+"/router:0")
	if err := os.WriteFile(config, []byte(newManifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statePath(config)); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(journalPath(plan), encoded(journal)); err != nil {
		t.Fatal(err)
	}
	before := len(backend.writes)
	if _, err := service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(backend.writes[before:], []string{"POST /orgs/org_a/apps/app_a/routing-keys/router_1/rotate"}) {
		t.Fatalf("unexpected recovery writes: %v", backend.writes[before:])
	}
	desired, _, _, err := loadManifest(config)
	if err != nil || desired.Routers[0].text("id") != "rotated_router_1" {
		t.Fatalf("replacement routing ID not recorded: %v", err)
	}
}

func TestResumeAfterFinalManifestWriteDoesNotRepeatMutations(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	var plan Plan
	_, _, _ = loadJSON(path, &plan)
	var journal Journal
	_, _, _ = loadJSON(journalPath(plan), &journal)
	journal.Complete = false
	if err := replaceFile(journalPath(plan), encoded(journal)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statePath(config)); err != nil {
		t.Fatal(err)
	}
	before := len(backend.writes)
	if _, err := service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatal(err)
	}
	if len(backend.writes) != before {
		t.Fatal("final file recovery repeated mutations")
	}
	if _, err := os.Stat(statePath(config)); err != nil {
		t.Fatal("final state was not restored")
	}
}

func TestEmptySectionsRevokeOnlyLocallyManagedResources(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	backend.connections["unrelated"] = fields(backend.connections["conn_1"])
	var document Fields
	data, _ := os.ReadFile(config)
	_ = json.Unmarshal(data, &document)
	document.set("keys", []any{})
	document.set("routingKeys", []any{})
	_ = os.WriteFile(config, encoded(document), 0600)
	result, err := service.Plan(context.Background(), "", config, path+".delete")
	if err != nil {
		t.Fatal(err)
	}
	if result.Operations != 3 {
		t.Fatalf("expected three managed revocations, got %d", result.Operations)
	}
	before := len(backend.writes)
	if _, err := service.Apply(context.Background(), "", path+".delete", true, false); err == nil {
		t.Fatal("missing destructive approval accepted")
	}
	if len(backend.writes) != before {
		t.Fatal("deletion ran without approval")
	}
	if _, err := service.Apply(context.Background(), "", path+".delete", true, true); err != nil {
		t.Fatal(err)
	}
	if backend.connections["unrelated"].text("status") != "active" {
		t.Fatal("unrelated resource revoked")
	}
}

func TestOmittedSectionsPreserveManagedResources(t *testing.T) {
	service, backend, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	_ = os.WriteFile(config, []byte(`{"version":1,"app":{"id":"app_a"}}`), 0600)
	before := len(backend.writes)
	planAndApply(t, service, config, path+".omitted")
	if len(backend.writes) != before {
		t.Fatal("omission changed resources")
	}
	state, _, err := readState(config, Target{Origin: "https://api.example.test", Org: "org_a", User: "user_a"}, "app_a")
	if err != nil || len(state.Keys) != 2 || len(state.Routers) != 1 {
		t.Fatalf("managed mappings lost: %v", err)
	}
}

func TestDecimalComparisonAndSecretErrors(t *testing.T) {
	if !sameFields(fields(map[string]string{"amount": "100.00"}), fields(map[string]string{"amount": "100"})) {
		t.Fatal("equivalent decimal values differ")
	}
	if sameFields(fields(map[string]string{"amount": "1.000000000000000001"}), fields(map[string]string{"amount": "1"})) {
		t.Fatal("decimal precision ignored")
	}
	if secretMissing(errors.New("credential store locked")) {
		t.Fatal("store error treated as a missing credential")
	}
}
