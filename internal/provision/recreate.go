package provision

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func (service *Service) planningManifest(config string) (Manifest, []byte, []byte, error) {
	desired, original, canonical, err := service.resolvedManifest(config)
	if err != nil {
		return desired, original, canonical, err
	}
	var state State
	exists, _, err := loadJSON(statePath(config), &state)
	if err != nil || !exists || !state.Destroyed {
		return desired, original, canonical, err
	}
	ids, err := service.destroyedResourceIDs(config, state)
	if err != nil {
		return Manifest{}, nil, nil, err
	}
	canonical, err = recreationManifest(canonical, state, ids)
	if err != nil {
		return Manifest{}, nil, nil, err
	}
	if err := json.Unmarshal(canonical, &desired); err != nil {
		return Manifest{}, nil, nil, err
	}
	return desired, original, canonical, nil
}

func removedResourceIDs(plan Plan) []string {
	ids := []string{}
	for _, step := range plan.Steps {
		switch step.Kind {
		case "delete_app", "revoke_key", "revoke_router", "revoke_service", "delete_provider":
			ids = append(ids, step.ID)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

func (service *Service) destroyedResourceIDs(config string, state State) ([]string, error) {
	ids := slices.Clone(state.DestroyedIDs)
	ids = append(ids, state.AppID)
	ids = append(ids, state.Routers...)
	for _, id := range state.Keys {
		ids = append(ids, id)
	}
	if workspace := service.workspace; workspace != nil && workspace.snapshot.Operation != nil {
		operation := workspace.snapshot.Operation
		if operation.Plan.Destroy && operation.Journal.Complete {
			ids = append(ids, removedResourceIDs(operation.Plan)...)
		}
		ids = append(ids, operation.Plan.DestroyedIDs...)
	}
	// Earlier destroy versions recorded shared IDs in the saved plan rather than state.
	if service.workspace == nil && config != "" && len(state.DestroyedIDs) == 0 {
		path := strings.TrimSuffix(config, filepath.Ext(config)) + ".destroy.plan.json"
		var plan Plan
		if exists, _, err := loadJSON(path, &plan); err != nil {
			return nil, err
		} else if exists && plan.Destroy && operationID.MatchString(plan.ID) && plan.Config == config && plan.Bindings["app"] == state.AppID && plan.Target == state.Target {
			var journal Journal
			if exists, _, err := loadJSON(journalPath(plan), &journal); err != nil {
				return nil, err
			} else if exists && journal.Complete && journal.PlanDigest == planDigest(plan) {
				ids = append(ids, removedResourceIDs(plan)...)
			}
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	return slices.DeleteFunc(ids, func(id string) bool { return id == "" }), nil
}

// Keep the user's settings. Only IDs confirmed removed by destroy are cleared.
func recreationManifest(data []byte, state State, destroyedIDs []string) ([]byte, error) {
	var document Fields
	if json.Unmarshal(data, &document) != nil {
		return nil, storageError()
	}
	if org := document.text("organizationId"); org != "" && org != state.Target.Org {
		return nil, problem.New("organization_mismatch", "The manifest's organization differs from the destroyed deployment's organization.", 4)
	}
	document.set("organizationId", state.Target.Org)
	removed := map[string]bool{}
	for _, id := range destroyedIDs {
		removed[id] = true
	}
	var app Fields
	_ = json.Unmarshal(document["app"], &app)
	if removed[app.text("id")] {
		delete(app, "id")
	}
	document.set("app", app)
	for _, section := range []string{"keys", "routingKeys", "serviceKeys", "providerCredentials"} {
		if !document.has(section) {
			continue
		}
		var resources map[string]Fields
		if json.Unmarshal(document[section], &resources) != nil {
			return nil, storageError()
		}
		for _, resource := range resources {
			if removed[resource.text("id")] {
				delete(resource, "id")
			}
		}
		document.set(section, resources)
	}
	resolved := encoded(document)
	if _, err := manifest.Parse(resolved); err != nil {
		failure := problem.Safe(err)
		failure.Code = "recreation_settings_required"
		failure.Message = "Add creation settings for the destroyed resources in this manifest, then plan again. " + failure.Message
		return nil, failure
	}
	return resolved, nil
}
