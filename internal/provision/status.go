package provision

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

type FieldChange struct {
	Field   string          `json:"field"`
	Actual  json.RawMessage `json:"actual"`
	Desired json.RawMessage `json:"desired"`
}

type ResourceStatus struct {
	Ref          string        `json:"ref"`
	ID           string        `json:"id,omitempty"`
	Status       string        `json:"status"`
	Matches      bool          `json:"matches"`
	Changes      []FieldChange `json:"changes"`
	SecretStatus string        `json:"secret_status,omitempty"`
	Notes        []string      `json:"notes,omitempty"`
}

type ApplyStatus struct {
	Operation string `json:"operation"`
	Journal   string `json:"journal"`
	Complete  bool   `json:"complete"`
	Completed int    `json:"completed_steps"`
	Total     int    `json:"total_steps"`
}

type StatusResult struct {
	Config    string           `json:"config"`
	Target    Target           `json:"target"`
	InSync    bool             `json:"in_sync"`
	Resources []ResourceStatus `json:"resources"`
	LastApply *ApplyStatus     `json:"last_apply,omitempty"`
}

func (service *Service) Status(ctx context.Context, origin, config string) (StatusResult, error) {
	config, err := filepath.Abs(config)
	if err != nil {
		return StatusResult{}, storageError()
	}
	desired, _, _, err := loadManifest(config)
	if err != nil {
		return StatusResult{}, err
	}
	remote := &gateway{ctx: ctx, service: service, target: Target{Origin: origin}}
	if err := remote.identify(); err != nil {
		return StatusResult{}, err
	}
	state, _, err := readState(config, remote.target, desired.App.text("id"))
	if err != nil {
		return StatusResult{}, err
	}
	last, err := lastApply(config)
	if err != nil {
		return StatusResult{}, err
	}
	result := StatusResult{Config: config, Target: remote.target, InSync: true, Resources: []ResourceStatus{}, LastApply: last}
	app, appStatus, err := inspectApp(remote, desired.App)
	if err != nil {
		return StatusResult{}, err
	}
	result.Resources = append(result.Resources, appStatus)
	aliases := map[string]string{}
	for alias, id := range state.Keys {
		aliases["key:"+alias] = id
	}
	for _, key := range desired.Keys {
		aliases["key:"+key.text("routingAlias")] = key.text("id")
	}
	for _, key := range desired.Keys {
		status, err := inspectKey(remote, app, desired.App, key)
		if err != nil {
			return StatusResult{}, err
		}
		result.Resources = append(result.Resources, status)
	}
	for index, router := range desired.Routers {
		if router.text("id") != "" && app.ID == "" {
			status := resourceStatus(desired.resourceRef("routingKeys", index), router.text("id"))
			status.Status, status.Notes = "unchecked", []string{"The parent app could not be found."}
			result.Resources = append(result.Resources, status)
			continue
		}
		status, err := inspectRouter(remote, desired.App.text("id"), desired.resourceRef("routingKeys", index), router, aliases)
		if err != nil {
			return StatusResult{}, err
		}
		result.Resources = append(result.Resources, status)
	}
	removed, err := inspectRemovals(remote, desired, state)
	if err != nil {
		return StatusResult{}, err
	}
	result.Resources = append(result.Resources, removed...)
	for index, key := range desired.ServiceKeys {
		status, err := inspectService(remote, desired.resourceRef("serviceKeys", index), key)
		if err != nil {
			return StatusResult{}, err
		}
		result.Resources = append(result.Resources, status)
	}
	for index, credential := range desired.Providers {
		status, err := inspectProvider(remote, desired.resourceRef("providerCredentials", index), credential)
		if err != nil {
			return StatusResult{}, err
		}
		result.Resources = append(result.Resources, status)
	}
	for _, resource := range result.Resources {
		if !resource.Matches {
			result.InSync = false
		}
	}
	if last != nil && !last.Complete {
		result.InSync = false
	}
	return result, nil
}

func resourceStatus(ref, id string) ResourceStatus {
	return ResourceStatus{Ref: ref, ID: id, Status: "no_id", Changes: []FieldChange{}}
}

func (status *ResourceStatus) readError(err error) error {
	if err == nil {
		return nil
	}
	if problem.Safe(err).Code != "resource_not_found" {
		return err
	}
	status.Status = "not_found"
	return nil
}

func (status *ResourceStatus) compare(actualStatus string, actual, desired Fields) {
	status.Status = actualStatus
	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if sameFields(actual.pick(name), desired.pick(name)) {
			continue
		}
		status.Changes = append(status.Changes, FieldChange{Field: name, Actual: actual.pick(name)[name], Desired: desired[name]})
	}
	status.Matches = len(status.Changes) == 0
}

func inspectApp(remote *gateway, desired Fields) (api.ManagedApp, ResourceStatus, error) {
	status := resourceStatus("app", desired.text("id"))
	if status.ID == "" {
		return api.ManagedApp{}, status, nil
	}
	var current api.ManagedApp
	err := remote.call([]string{"apps:read"}, func(client *api.Client, login auth.Login) error {
		var err error
		current, err = client.GetApp(remote.ctx, login.APIOrigin, login.Organization, status.ID, login.AccessToken)
		return err
	})
	if readErr := status.readError(err); readErr != nil {
		return current, status, readErr
	}
	if err != nil {
		return current, status, nil
	}
	payload := appProfile(desired)
	if desired.has("allowedModels") {
		payload["allowed_models"] = desired["allowedModels"]
	}
	if desired.has("allowedScopes") {
		payload["allowed_scopes"] = desired["allowedScopes"]
	}
	var oauth Fields
	_ = json.Unmarshal(desired["oauth"], &oauth)
	if oauth.has("clientType") {
		payload["client_type"] = oauth["clientType"]
	}
	if oauth.has("redirectUris") {
		payload["redirect_uris"] = oauth["redirectUris"]
	}
	status.compare(current.Status, appFields(current), payload)
	if current.Status == "suspended" || current.Status == "deleted" {
		status.Matches = false
	}
	return current, status, nil
}

func inspectKey(remote *gateway, app api.ManagedApp, desiredApp, desired Fields) (ResourceStatus, error) {
	status := resourceStatus("key:"+desired.text("routingAlias"), desired.text("id"))
	if status.ID == "" {
		return status, nil
	}
	if app.ID == "" {
		status.Status = "unchecked"
		status.Notes = []string{"The parent app could not be found."}
		return status, nil
	}
	current, err := remote.key(desiredApp.text("id"), status.ID)
	if readErr := status.readError(err); readErr != nil {
		return status, readErr
	}
	if err != nil {
		return status, nil
	}
	payload := fields(map[string]string{"status": "active"})
	for source, destination := range map[string]string{"name": "key_alias", "creditMode": "credit_mode", "overrunPolicy": "overrun_policy", "productUserChargeMultiplier": "product_user_charge_multiplier"} {
		if value, exists := desired[source]; exists {
			payload[destination] = value
		}
	}
	models := desired.strings("allowedModels")
	if models == nil {
		models = desiredApp.strings("allowedModels")
	}
	if models == nil {
		models = app.AllowedModels
	}
	if models != nil {
		payload.set("allowed_models", models)
	}
	actual := keyFields(current)
	if desired.has("cap") {
		payload["cap"] = desired["cap"]
		actual.set("cap", capFields(current))
	}
	status.compare(current.detail.Connection.Status, actual, payload)
	if current.credential == nil || current.credential.Status != "active" {
		status.Matches = false
		status.Notes = []string{"This connection has no active inference credential."}
	}
	return status, nil
}

func inspectRouter(remote *gateway, appID, ref string, desired Fields, aliases map[string]string) (ResourceStatus, error) {
	status := resourceStatus(ref, desired.text("id"))
	if status.ID == "" {
		return status, nil
	}
	current, err := remote.router(appID, status.ID)
	if readErr := status.readError(err); readErr != nil {
		return status, readErr
	}
	if err != nil {
		return status, nil
	}
	payload := fields(map[string]string{"status": "active"})
	if desired.has("name") {
		payload["key_alias"] = desired["name"]
	}
	if desired.has("defaultSelector") {
		payload["default_selector_alias"] = desired["defaultSelector"]
	}
	if desired.has("members") {
		members := []Member{}
		for _, alias := range desired.strings("members") {
			members = append(members, Member{Ref: "key:" + alias, Alias: alias})
		}
		slices.SortFunc(members, func(left, right Member) int { return strings.Compare(left.Alias, right.Alias) })
		payload.set("members", plannedMembers(members, aliases))
	}
	status.compare(current.Status, routerFields(current), payload)
	return status, nil
}

func inspectService(remote *gateway, ref string, desired Fields) (ResourceStatus, error) {
	status := resourceStatus(ref, desired.text("id"))
	if status.ID == "" {
		return status, nil
	}
	current, err := remote.serviceKey(status.ID)
	if readErr := status.readError(err); readErr != nil {
		return status, readErr
	}
	if err != nil {
		return status, nil
	}
	payload := Fields{}
	if desired.has("name") {
		payload["key_alias"] = desired["name"]
	}
	if desired.has("scopes") {
		payload["scopes"] = desired["scopes"]
	}
	payload.set("status", "active")
	status.compare(current.Status, fields(current), payload)
	if !serviceKeyActive(current, remote.service.Auth.Now()) && current.Status == "active" {
		status.Status, status.Matches = "expired", false
	}
	return status, nil
}

func inspectProvider(remote *gateway, ref string, desired Fields) (ResourceStatus, error) {
	status := resourceStatus(ref, desired.text("id"))
	if status.ID == "" {
		return status, nil
	}
	current, err := remote.provider(status.ID)
	if readErr := status.readError(err); readErr != nil {
		return status, readErr
	}
	if err != nil {
		return status, nil
	}
	payload := providerMetadata(desired)
	payload.set("status", "active")
	status.compare(current.Status, fields(current), payload)
	if !desired.has("secretFromEnv") {
		return status, nil
	}
	value, exists := os.LookupEnv(desired.text("secretFromEnv"))
	if !exists || strings.TrimSpace(value) == "" {
		status.SecretStatus = "unverified"
		status.Matches = false
		status.Notes = []string{"Set secretFromEnv's variable to compare the provider input. Secret values are never printed."}
		return status, nil
	}
	changed, err := remote.providerInputChanged(current, desired.text("secretFromEnv"), providerSecretKind(desired))
	if err != nil {
		return status, err
	}
	status.SecretStatus = "matches"
	if changed {
		status.SecretStatus = "differs"
		status.Matches = false
	}
	return status, nil
}

func lastApply(config string) (*ApplyStatus, error) {
	entries, err := os.ReadDir(directory(config))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError()
	}
	var latest *ApplyStatus
	var latestTime int64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".apply.json") {
			continue
		}
		path := filepath.Join(directory(config), entry.Name())
		var journal Journal
		_, _, err := loadJSON(path, &journal)
		if err != nil {
			return nil, err
		}
		if journal.Config != config {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, storageError()
		}
		if latest != nil && info.ModTime().UnixNano() < latestTime {
			continue
		}
		latestTime = info.ModTime().UnixNano()
		latest = &ApplyStatus{Operation: strings.TrimSuffix(entry.Name(), ".apply.json"), Journal: path, Complete: journal.Complete, Total: len(journal.Steps)}
		for _, step := range journal.Steps {
			if step.Done {
				latest.Completed++
			}
		}
	}
	return latest, nil
}

func inspectRemovals(remote *gateway, desired Manifest, state State) ([]ResourceStatus, error) {
	result := []ResourceStatus{}
	if desired.Keys != nil {
		ids := []string{}
		for _, id := range state.Keys {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		for _, id := range ids {
			if slices.ContainsFunc(desired.Keys, func(key Fields) bool { return key.text("id") == id }) {
				continue
			}
			status := resourceStatus("removed_key:"+id, id)
			current, err := remote.key(state.AppID, id)
			if readErr := status.readError(err); readErr != nil {
				return nil, readErr
			}
			if err == nil {
				status.compare(current.detail.Connection.Status, keyFields(current), fields(map[string]string{"status": "revoked"}))
			} else {
				status.Matches = true
			}
			result = append(result, status)
		}
	}
	if desired.Routers != nil {
		for _, id := range state.Routers {
			if slices.ContainsFunc(desired.Routers, func(router Fields) bool { return router.text("id") == id }) {
				continue
			}
			status := resourceStatus("removed_router:"+id, id)
			current, err := remote.router(state.AppID, id)
			if readErr := status.readError(err); readErr != nil {
				return nil, readErr
			}
			if err == nil {
				status.compare(current.Status, routerFields(current), fields(map[string]string{"status": "revoked"}))
			} else {
				status.Matches = true
			}
			result = append(result, status)
		}
	}
	return result, nil
}
