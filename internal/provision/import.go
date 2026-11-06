package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

type ImportResult struct {
	Config  string `json:"config"`
	AppID   string `json:"app_id"`
	Keys    int    `json:"keys"`
	Routers int    `json:"routing_keys"`
}

func (service *Service) Import(ctx context.Context, origin, appID, config string) (ImportResult, error) {
	if !resourceID.MatchString(appID) {
		return ImportResult{}, problem.New("invalid_app_id", "Provide an existing app ID with --app-id.", 2)
	}
	config, err := filepath.Abs(config)
	if err != nil {
		return ImportResult{}, storageError()
	}
	unlock, err := lockConfig(ctx, config)
	if err != nil {
		return ImportResult{}, err
	}
	defer unlock()
	if err := requireNewImport(config); err != nil {
		return ImportResult{}, err
	}
	remote := &gateway{ctx: ctx, service: service, target: Target{Origin: origin}}
	if err := remote.identify(); err != nil {
		return ImportResult{}, err
	}
	app, err := remote.app(appID)
	if err != nil {
		return ImportResult{}, err
	}
	keys, routers, err := remote.importResources(appID)
	if err != nil {
		return ImportResult{}, err
	}
	aliases, err := importAliases(keys, routers)
	if err != nil {
		return ImportResult{}, err
	}
	document := fields(map[string]any{"version": 1, "app": importedApp(app), "keys": []Fields{}, "routingKeys": []Fields{}})
	importedKeys := []Fields{}
	state := State{Version: 1, Target: remote.target, AppID: appID, Keys: map[string]string{}, Routers: []string{}}
	for _, key := range keys {
		id := key.detail.Connection.ID
		importedKeys = append(importedKeys, importedKey(key, aliases[id]))
		state.Keys[aliases[id]] = id
	}
	importedRouters := []Fields{}
	for _, router := range routers {
		members := []string{}
		for _, member := range router.Members {
			members = append(members, aliases[member.ConnectionID])
		}
		slices.Sort(members)
		entry := fields(map[string]any{"id": router.KeyID, "members": members, "defaultSelector": router.DefaultSelector})
		if router.Name != nil {
			entry.set("name", *router.Name)
		}
		importedRouters = append(importedRouters, entry)
		state.Routers = append(state.Routers, router.KeyID)
	}
	document.set("keys", importedKeys)
	document.set("routingKeys", importedRouters)
	data := encoded(document)
	if _, err := manifest.Parse(data); err != nil {
		return ImportResult{}, err
	}
	if err := writeNew(statePath(config), encoded(state)); err != nil {
		return ImportResult{}, err
	}
	if err := writeNew(config, data); err != nil {
		_ = os.Remove(statePath(config))
		return ImportResult{}, err
	}
	return ImportResult{Config: config, AppID: appID, Keys: len(keys), Routers: len(routers)}, nil
}

func requireNewImport(config string) error {
	for _, path := range []string{config, statePath(config)} {
		_, err := os.Lstat(path)
		if !errors.Is(err, os.ErrNotExist) {
			return problem.New("destination_exists", "Import requires a new manifest path without existing CLI state.", 2)
		}
	}
	return requireFinishedApply(config)
}

func (remote *gateway) importResources(appID string) ([]keyState, []api.RoutingKey, error) {
	var connections []api.AppConnection
	var routers []api.RoutingKey
	err := remote.call([]string{"app_connections:read", "routing_keys:read"}, func(client *api.Client, login auth.Login) error {
		var err error
		connections, err = client.ListAppConnections(remote.ctx, login.APIOrigin, login.Organization, appID, login.AccessToken, 1000)
		if err != nil {
			return err
		}

		routers, err = client.ListRoutingKeys(remote.ctx, login.APIOrigin, login.Organization, appID, login.AccessToken)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	slices.SortFunc(connections, func(left, right api.AppConnection) int { return strings.Compare(left.ID, right.ID) })
	slices.SortFunc(routers, func(left, right api.RoutingKey) int { return strings.Compare(left.KeyID, right.KeyID) })
	keys := []keyState{}
	for _, connection := range connections {
		if connection.Status != "active" {
			continue
		}
		key, err := remote.key(appID, connection.ID)
		if err != nil {
			return nil, nil, err
		}
		if key.credential == nil || key.credential.Status != "active" {
			return nil, nil, problem.New("import_key_unavailable", "An active connection has no active inference credential. Resolve it before importing.", 2)
		}
		keys = append(keys, key)
	}
	routers = slices.DeleteFunc(routers, func(router api.RoutingKey) bool { return router.Status != "active" })
	return keys, routers, nil
}

func importAliases(keys []keyState, routers []api.RoutingKey) (map[string]string, error) {
	byConnection := map[string]string{}
	byAlias := map[string]string{}
	credentials := map[string]string{}
	for _, key := range keys {
		credentials[key.detail.Connection.ID] = key.credential.ID
	}
	for _, router := range routers {
		for _, member := range router.Members {
			if credentials[member.ConnectionID] == "" || credentials[member.ConnectionID] != member.KeyID || member.Status != "active" {
				return nil, problem.New("import_member_unavailable", "A router references a credential outside the active programmatic connections. Resolve that membership before importing.", 2)
			}
			if (byConnection[member.ConnectionID] != "" && byConnection[member.ConnectionID] != member.Alias) || (byAlias[member.Alias] != "" && byAlias[member.Alias] != member.ConnectionID) {
				return nil, problem.New("import_alias_conflict", "Existing routing selectors cannot be represented by one distinct alias per connection in this manifest. Resolve the conflicting selectors before importing; no routes were changed.", 2)
			}
			byConnection[member.ConnectionID], byAlias[member.Alias] = member.Alias, member.ConnectionID
		}
	}
	// Existing selectors win. Only unrouted connections receive generated aliases.
	next := 1
	for _, key := range keys {
		id := key.detail.Connection.ID
		if byConnection[id] != "" {
			continue
		}
		alias := "key_" + strconv.Itoa(next)
		for byAlias[alias] != "" {
			next++
			alias = "key_" + strconv.Itoa(next)
		}
		byConnection[id], byAlias[alias] = alias, id
		next++
	}
	return byConnection, nil
}

func importedApp(app api.ManagedApp) Fields {
	result := fields(map[string]any{"id": app.ID, "name": app.Name, "allowedModels": app.AllowedModels, "allowedScopes": app.AllowedScopes, "creditUnitsPerUsd": app.CreditUnitsPerUSD})
	if app.ProductUserChargeMultiplier != "" {
		result.set("productUserChargeMultiplier", app.ProductUserChargeMultiplier)
	}
	result.set("oauth", map[string]any{"clientType": app.ClientType, "redirectUris": appFields(app).strings("redirect_uris")})
	var cap any
	if app.DefaultCapAmount != nil {
		cap = map[string]any{"amount": app.DefaultCapAmount, "currency": app.DefaultCapCurrency, "period": app.DefaultCapPeriod}
	}
	result.set("consent", map[string]any{"defaultCap": cap, "minimumCapAmount": app.MinimumCapAmount, "maximumCapAmount": app.MaximumCapAmount, "allowUnlimitedCap": app.AllowUnlimitedCap})
	return result
}

func importedKey(key keyState, alias string) Fields {
	connection := key.detail.Connection
	result := fields(map[string]any{"id": connection.ID, "routingAlias": alias, "allowedModels": key.credential.AllowedModels,
		"creditMode": connection.CreditMode, "overrunPolicy": connection.OverrunPolicy, "productUserChargeMultiplier": connection.ProductUserChargeMultiplier, "cap": capFields(key)})
	if key.credential.Name != nil {
		result.set("name", *key.credential.Name)
	}
	return result
}
