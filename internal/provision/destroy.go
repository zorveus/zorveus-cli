package provision

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func (service *Service) PlanDestroy(ctx context.Context, origin, config, output string) (result PlanResult, err error) {
	config, err = filepath.Abs(config)
	if err != nil {
		return PlanResult{}, storageError()
	}
	unlock, err := lockConfig(ctx, config)
	if err != nil {
		return PlanResult{}, err
	}
	defer unlock()
	service, close, err := service.openWorkspace(ctx, config, nil)
	if err != nil {
		return PlanResult{}, err
	}
	defer closeWorkspace(close, &err)
	plan, pending, err := service.pendingDestroy(config, output)
	if err != nil {
		return PlanResult{}, err
	}
	if pending && (origin != "" && origin != plan.Target.Origin || service.Auth.Organization != "" && service.Auth.Organization != plan.Target.Org) {
		return PlanResult{}, conflict()
	}
	if !pending {
		if err := requireFinishedApply(config); err != nil {
			return PlanResult{}, err
		}
		plan, err = service.buildDestroy(ctx, origin, config)
		if err != nil {
			return PlanResult{}, err
		}
	}
	if err := savePlan(output, plan); err != nil {
		return PlanResult{}, err
	}
	changes := []Step{}
	for _, step := range plan.Steps {
		if stepChanges(step) {
			changes = append(changes, step)
		}
	}
	return PlanResult{Destroy: true, Resuming: pending, Target: &plan.Target, Plan: output, Operations: len(changes), Changes: changes, RequiredScopes: plan.Scopes}, nil
}

func (service *Service) pendingDestroy(config, output string) (Plan, bool, error) {
	var plan Plan
	if workspace := service.workspace; workspace != nil && workspace.snapshot.Operation != nil && !workspace.snapshot.Operation.Journal.Complete {
		plan = relocatePlan(workspace.snapshot.Operation.Plan, config)
	} else if _, _, err := loadJSON(output, &plan); err != nil {
		return Plan{}, false, err
	}
	if !plan.Destroy || plan.Config != config || !operationID.MatchString(plan.ID) {
		return Plan{}, false, nil
	}
	var journal Journal
	exists, _, err := loadJSON(journalPath(plan), &journal)
	if err != nil || !exists || journal.Complete {
		return Plan{}, false, err
	}
	data, err := os.ReadFile(config)
	if err != nil {
		return Plan{}, false, storageError()
	}
	if journal.PlanDigest != planDigest(plan) || digest(data) != plan.ConfigDigest && digest(data) != journal.FinalDigest {
		return Plan{}, false, conflict()
	}
	return plan, true, nil
}

func (service *Service) buildDestroy(ctx context.Context, origin, config string) (Plan, error) {
	desired, original, canonical, err := service.resolvedManifest(config)
	if err != nil {
		return Plan{}, err
	}
	target, err := ProjectTarget(config, origin, desired.OrganizationID)
	if err != nil {
		return Plan{}, err
	}
	if _, _, err := readState(config, target, desired.App.text("id")); err != nil {
		return Plan{}, err
	}
	remote := &gateway{ctx: ctx, service: service, target: target}
	if err := remote.identify(); err != nil {
		return Plan{}, err
	}
	state, stateBytes, err := readState(config, remote.target, desired.App.text("id"))
	if err != nil {
		return Plan{}, err
	}
	var document Fields
	_ = json.Unmarshal(canonical, &document)
	document.set("organizationId", remote.target.Org)
	plan := Plan{Version: 1, Destroy: true, ID: newID(), Target: remote.target, Config: config, ConfigDigest: digest(original), StateDigest: digest(stateBytes), Manifest: encoded(document), Bindings: map[string]string{"app": desired.App.text("id")}, Steps: []Step{}}
	if workspace := service.workspace; workspace != nil {
		plan.RemoteStateID, plan.RemoteRevision = workspace.session.Document.ID, workspace.session.Document.Revision
		selected := workspace.config
		plan.StateBackend = &selected
	}
	appStatus := "deleted"
	if appID := desired.App.text("id"); appID != "" && !state.Destroyed {
		var app api.ManagedApp
		err := remote.call([]string{"apps:read"}, func(client *api.Client, login auth.Login) error {
			var err error
			app, err = client.GetApp(ctx, login.APIOrigin, login.Organization, appID, login.AccessToken)
			return err
		})
		if err != nil && problem.Safe(err).Code != "resource_not_found" {
			return Plan{}, err
		}
		if err == nil && app.Status != "deleted" {
			appStatus = app.Status
			if err := plan.planAppDestruction(remote, desired, state); err != nil {
				return Plan{}, err
			}
		}
	}
	// Only explicitly listed organization credentials are included, never an org-wide sweep.
	for index, key := range desired.ServiceKeys {
		if id := key.text("id"); id != "" {
			current, err := remote.serviceKey(id)
			if err != nil && problem.Safe(err).Code != "resource_not_found" {
				return Plan{}, err
			}
			status := current.Status
			if err != nil {
				status = "revoked"
			}
			plan.addDestroyStep("revoke_service", desired.resourceRef("serviceKeys", index), id, status, "revoked")
		}
	}
	for index, credential := range desired.Providers {
		if id := credential.text("id"); id != "" {
			current, err := remote.provider(id)
			if err != nil && problem.Safe(err).Code != "resource_not_found" {
				return Plan{}, err
			}
			status := current.Status
			if err != nil {
				status = "deleted"
			}
			plan.addDestroyStep("delete_provider", desired.resourceRef("providerCredentials", index), id, status, "deleted")
		}
	}
	if id := desired.App.text("id"); id != "" {
		// Delete last: child routes stop being accessible after the app is deleted.
		plan.addDestroyStep("delete_app", "app", id, appStatus, "deleted")
	}
	plan.Scopes = planScopes(plan)
	return plan, nil
}

func (plan *Plan) addDestroyStep(kind, ref, id, before, after string) {
	plan.Bindings[ref] = id
	plan.Steps = append(plan.Steps, Step{Kind: kind, Ref: ref, ID: id, Before: fields(map[string]string{"status": before}), After: fields(map[string]string{"status": after}), Destructive: true})
}

func (plan *Plan) planAppDestruction(remote *gateway, desired Manifest, state State) error {
	appID := plan.Bindings["app"]
	routers := map[string]string{}
	keys := map[string]string{}
	for _, id := range state.Routers {
		routers[id] = "router:" + id
	}
	for alias, id := range state.Keys {
		keys[id] = "key:" + alias
	}
	err := remote.call([]string{"routing_keys:read", "app_connections:read"}, func(client *api.Client, login auth.Login) error {
		listedRouters, err := client.ListRoutingKeys(remote.ctx, login.APIOrigin, login.Organization, appID, login.AccessToken)
		if err != nil {
			return err
		}
		for _, router := range listedRouters {
			if _, exists := routers[router.KeyID]; !exists {
				routers[router.KeyID] = "router:" + router.KeyID
			}
		}
		connections, err := client.ListAppConnections(remote.ctx, login.APIOrigin, login.Organization, appID, login.AccessToken, 1000)
		if err != nil {
			return err
		}
		for _, key := range connections {
			if _, exists := keys[key.ID]; !exists {
				keys[key.ID] = "key:" + key.ID
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for index, router := range desired.Routers {
		if id := router.text("id"); id != "" {
			routers[id] = desired.resourceRef("routingKeys", index)
		}
	}
	for index, key := range desired.Keys {
		if id := key.text("id"); id != "" {
			keys[id] = desired.resourceRef("keys", index)
		}
	}
	for _, id := range sortedDestroyIDs(routers) {
		current, err := remote.router(appID, id)
		if err != nil {
			if problem.Safe(err).Code == "resource_not_found" {
				continue
			}
			return err
		}
		plan.addDestroyStep("revoke_router", routers[id], id, current.Status, "revoked")
	}
	for _, id := range sortedDestroyIDs(keys) {
		current, err := remote.key(appID, id)
		if err != nil {
			if problem.Safe(err).Code == "resource_not_found" {
				continue
			}
			return err
		}
		plan.Bindings[keys[id]] = id
		if cap := current.detail.Cap; cap != nil && cap.Status == "active" {
			if !resourceID.MatchString(cap.ID) {
				return problem.New("invalid_response", "The API returned an invalid connection cap ID.", 1)
			}
			plan.Steps = append(plan.Steps, Step{Kind: "key_cap", Ref: keys[id], ID: id, CapID: cap.ID, Before: capFields(current), After: nil, Destructive: true})
		}
		plan.addDestroyStep("revoke_key", keys[id], id, current.detail.Connection.Status, "revoked")
	}
	return nil
}

func sortedDestroyIDs(resources map[string]string) []string {
	ids := make([]string, 0, len(resources))
	for id := range resources {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
