package provision

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func (service *Service) Plan(ctx context.Context, origin, config, output string) (PlanResult, error) {
	config, err := filepath.Abs(config)
	if err != nil {
		return PlanResult{}, storageError()
	}
	unlock, err := lockConfig(ctx, config)
	if err != nil {
		return PlanResult{}, err
	}
	defer unlock()
	if err := requireFinishedApply(config); err != nil {
		return PlanResult{}, err
	}
	plan, err := service.build(ctx, origin, config)
	if err != nil {
		return PlanResult{}, err
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
	return PlanResult{Plan: output, Operations: len(changes), Changes: changes, RequiredScopes: plan.Scopes}, nil
}

func savePlan(path string, plan Plan) error {
	output, err := filepath.Abs(path)
	if err != nil {
		return storageError()
	}
	if output == plan.Config || strings.HasPrefix(output, directory(plan.Config)+string(filepath.Separator)) {
		return problem.New("invalid_plan_output", "Save the plan separately from the manifest and its .zorveus state directory.", 2)
	}
	var previous Plan
	exists, _, err := loadJSON(output, &previous)
	if err != nil {
		return err
	}
	if exists {
		if previous.Version != 1 || !operationID.MatchString(previous.ID) || !filepath.IsAbs(previous.Config) {
			return problem.New("invalid_plan_output", "The output file is not a saved Zorveus plan. Choose another --out path.", 2)
		}
		if previous.Config != plan.Config {
			return problem.New("invalid_plan_output", "The output plan belongs to a different manifest. Use a separate --out filename.", 2)
		}
	}
	// Write only after planning succeeds, keeping the previous file until replacement.
	return replaceFile(output, encoded(plan))
}

func (service *Service) build(ctx context.Context, origin, config string) (Plan, error) {
	desired, original, canonical, err := loadManifest(config)
	if err != nil {
		return Plan{}, err
	}
	remote := &gateway{ctx: ctx, service: service, target: Target{Origin: origin}}
	if err := remote.identify(); err != nil {
		return Plan{}, err
	}
	state, stateBytes, err := readState(config, remote.target, desired.App.text("id"))
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Version: 1, ID: newID(), Target: remote.target, Config: config, ConfigDigest: digest(original), StateDigest: digest(stateBytes), Manifest: canonical, Steps: []Step{}, Bindings: map[string]string{}}
	models, err := plan.planApp(remote, desired.App)
	if err != nil {
		return Plan{}, err
	}
	if err := plan.planShared(remote, desired); err != nil {
		return Plan{}, err
	}
	if desired.Keys == nil {
		for alias, id := range state.Keys {
			plan.Bindings["key:"+alias] = id
		}
	}
	for index, key := range desired.Keys {
		if err := plan.planKey(remote, index, key, models); err != nil {
			return Plan{}, err
		}
	}
	for index, router := range desired.Routers {
		if err := plan.planRouter(remote, index, router); err != nil {
			return Plan{}, err
		}
	}
	if err := plan.removals(remote, desired, state); err != nil {
		return Plan{}, err
	}
	plan.Scopes = planScopes(plan)
	return plan, nil
}

func (plan *Plan) planApp(remote *gateway, app Fields) ([]string, error) {
	appID := app.text("id")
	plan.Bindings["app"] = appID
	models := app.strings("allowedModels")
	payload := appProfile(app)
	var oauth Fields
	_ = json.Unmarshal(app["oauth"], &oauth)
	if appID == "" {
		payload.set("allowed_models", models)
		clientType := oauth.text("clientType")
		if clientType == "" {
			clientType = "confidential"
		}
		payload.set("client_type", clientType)
		if oauth.has("redirectUris") {
			payload["redirect_uris"] = oauth["redirectUris"]
		}
		if app.has("allowedScopes") {
			payload["allowed_scopes"] = app["allowedScopes"]
		}
		plan.Steps = append(plan.Steps, Step{Kind: "create_app", Ref: "app", After: payload})
		return models, nil
	}
	current, err := remote.app(appID)
	if err != nil {
		return nil, err
	}
	before := appFields(current)
	if oauth.has("clientType") && oauth.text("clientType") != current.ClientType {
		return nil, manifestError("unsupported_update", "The existing app API does not change client type. Keep its current type or create a separate app.", "/app/oauth/clientType")
	}
	if app.has("allowedScopes") && !sameFields(before.pick("allowed_scopes"), Fields{"allowed_scopes": app["allowedScopes"]}) {
		return nil, manifestError("unsupported_update", "The existing app update API does not change allowed scopes.", "/app/allowedScopes")
	}
	if err := mergeConsent(payload, before); err != nil {
		return nil, err
	}
	if len(payload) > 0 {
		plan.add("app_profile", "app", appID, before, payload)
	}
	if models == nil {
		models = current.AllowedModels
	}
	plan.add("app_models", "app", appID, before, fields(map[string]any{"allowed_models": models}))
	plan.planRedirects(appID, current, oauth)
	return models, nil
}

func (plan *Plan) planKey(remote *gateway, index int, key Fields, models []string) error {
	ref := "key:" + key.text("routingAlias")
	id := key.text("id")
	plan.Bindings[ref] = id
	keyModels := key.strings("allowedModels")
	if keyModels == nil {
		keyModels = models
	}
	if err := requireModels(keyModels, models, "/keys/"+strconv.Itoa(index)+"/allowedModels"); err != nil {
		return err
	}
	policy := fields(map[string]any{"allowed_models": keyModels})
	if value, ok := key["name"]; ok {
		policy["key_alias"] = value
	}
	var before keyState
	if id == "" {
		policy.set("approved_scopes", []string{"inference:write"})
		policy.set("credit_mode", "enforce_if_present")
		policy.set("overrun_policy", "limit_output")
		for manifestName, apiName := range map[string]string{"creditMode": "credit_mode", "overrunPolicy": "overrun_policy", "productUserChargeMultiplier": "product_user_charge_multiplier"} {
			if value, ok := key[manifestName]; ok {
				policy[apiName] = value
			}
		}
		plan.Steps = append(plan.Steps, Step{Kind: "create_key", Ref: ref, After: policy})
	} else {
		var err error
		before, err = remote.key(plan.Bindings["app"], id)
		if err != nil {
			return err
		}
		if before.detail.Connection.Status != "active" || before.credential == nil || before.credential.Status != "active" {
			return conflict()
		}
		plan.add("key_policy", ref, id, keyFields(before), policy)
		for _, item := range []struct{ manifestName, apiName, kind string }{
			{"creditMode", "credit_mode", "key_credit"}, {"overrunPolicy", "overrun_policy", "key_overrun"}, {"productUserChargeMultiplier", "product_user_charge_multiplier", "key_pricing"},
		} {
			if value, ok := key[item.manifestName]; ok {
				plan.add(item.kind, ref, id, keyFields(before), Fields{item.apiName: value})
			}
		}
	}
	if key.has("cap") {
		var cap Fields
		_ = json.Unmarshal(key["cap"], &cap)
		step := Step{Kind: "key_cap", Ref: ref, ID: id, Before: capFields(before), After: cap, Destructive: cap == nil && before.detail.Cap != nil}
		if before.detail.Cap != nil {
			step.CapID = before.detail.Cap.ID
		}
		plan.Steps = append(plan.Steps, step)
	}
	return nil
}

func (plan *Plan) planRouter(remote *gateway, index int, router Fields) error {
	ref := "router:" + strconv.Itoa(index)
	id := router.text("id")
	plan.Bindings[ref] = id
	var before Fields
	if id != "" {
		current, err := remote.router(plan.Bindings["app"], id)
		if err != nil {
			return err
		}
		if current.Status != "active" {
			return conflict()
		}
		before = routerFields(current)
		if router.has("name") {
			plan.add("router_name", ref, id, before, Fields{"key_alias": router["name"]})
		}
	}
	if id != "" && !router.has("members") && !router.has("defaultSelector") {
		return nil
	}
	after := Fields{}
	step := Step{Kind: "router_members", Ref: ref, ID: id}
	if id == "" {
		step.Kind = "create_router"
		after["key_alias"] = router["name"]
	}
	if router.has("members") {
		for _, alias := range router.strings("members") {
			memberRef := "key:" + alias
			if _, ok := plan.Bindings[memberRef]; !ok {
				return manifestError("unknown_member", "Declare this key or restore its CLI alias mapping before planning.", "/routingKeys/"+strconv.Itoa(index)+"/members")
			}
			step.Members = append(step.Members, Member{Ref: memberRef, Alias: alias})
		}
	} else {
		var members []Fields
		_ = json.Unmarshal(before["members"], &members)
		for _, member := range members {
			step.Members = append(step.Members, Member{Ref: "id:" + member.text("connection_id"), Alias: member.text("selector_alias")})
		}
	}
	sort.Slice(step.Members, func(i, j int) bool { return step.Members[i].Alias < step.Members[j].Alias })
	after.set("default_selector_alias", nil)
	if value, ok := router["defaultSelector"]; ok {
		after["default_selector_alias"] = value
	} else if id != "" {
		after["default_selector_alias"] = before["default_selector_alias"]
	}
	if alias := after.text("default_selector_alias"); alias != "" && !slices.ContainsFunc(step.Members, func(m Member) bool { return m.Alias == alias }) {
		return manifestError("invalid_default_selector", "The default selector must identify a desired routing member.", "/routingKeys/"+strconv.Itoa(index)+"/defaultSelector")
	}
	after.set("members", plannedMembers(step.Members, plan.Bindings))
	step.After = after
	if id != "" {
		step.Before = before.pick("members", "default_selector_alias")
	}
	plan.Steps = append(plan.Steps, step)
	return nil
}

func (plan *Plan) add(kind, ref, id string, current, after Fields) {
	names := make([]string, 0, len(after))
	for name := range after {
		names = append(names, name)
	}
	plan.Steps = append(plan.Steps, Step{Kind: kind, Ref: ref, ID: id, Before: current.pick(names...), After: after})
}

func plannedMembers(members []Member, bindings map[string]string) []Fields {
	result := make([]Fields, 0, len(members))
	for _, member := range members {
		id := bindings[member.Ref]
		if len(member.Ref) > 3 && member.Ref[:3] == "id:" {
			id = member.Ref[3:]
		}
		if id == "" {
			id = member.Ref
		}
		result = append(result, fields(map[string]string{"connection_id": id, "selector_alias": member.Alias}))
	}
	return result
}

func (plan *Plan) removals(remote *gateway, desired Manifest, state State) error {
	if desired.Routers != nil {
		for _, id := range state.Routers {
			if slices.ContainsFunc(desired.Routers, func(r Fields) bool { return r.text("id") == id }) {
				continue
			}
			current, err := remote.router(plan.Bindings["app"], id)
			if err != nil {
				return err
			}
			plan.Steps = append(plan.Steps, Step{Kind: "revoke_router", Ref: "id:" + id, ID: id, Before: routerFields(current).pick("status"), After: fields(map[string]string{"status": "revoked"}), Destructive: true})
		}
	}
	if desired.Keys != nil {
		ids := []string{}
		for _, id := range state.Keys {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		for _, id := range ids {
			if slices.ContainsFunc(desired.Keys, func(k Fields) bool { return k.text("id") == id }) {
				continue
			}
			current, err := remote.key(plan.Bindings["app"], id)
			if err != nil {
				return err
			}
			plan.Steps = append(plan.Steps, Step{Kind: "revoke_key", Ref: "id:" + id, ID: id, Before: keyFields(current).pick("status"), After: fields(map[string]string{"status": "revoked"}), Destructive: true})
		}
	}
	return nil
}

func readState(config string, target Target, appID string) (State, []byte, error) {
	state := State{Version: 1, Target: target, Keys: map[string]string{}, Routers: []string{}}
	exists, data, err := loadJSON(statePath(config), &state)
	if err != nil {
		return state, nil, err
	}
	if exists && (state.Version != 1 || state.Target.Origin != target.Origin || state.Target.Org != target.Org || state.AppID != appID) {
		return state, nil, problem.New("state_target_mismatch", "This manifest's CLI state belongs to another app or login target. Use a separate manifest for a new app.", 4)
	}
	return state, data, nil
}

func stepChanges(step Step) bool {
	return step.Kind == "create_app" || step.Kind == "create_key" || step.Kind == "create_router" || !sameFields(step.Before, step.After)
}

func stepScopes(step Step) []string {
	scopes := []string{"apps:read"}
	switch step.Kind {
	case "create_app", "app_profile", "app_models", "app_redirect_add", "app_redirect_remove", "app_redirects":
		if stepChanges(step) {
			if step.Kind == "create_app" {
				scopes = append(scopes, "apps:create")
				if stepHasCredential(step) {
					scopes = append(scopes, "apps:rotate_secret")
				}
			} else {
				scopes = append(scopes, "apps:update")
			}
		}
		if stepChanges(step) && hasAppBilling(step.After) {
			scopes = append(scopes, "caps:app_write")
		}
	case "create_service", "service_check", "service_metadata":
		scopes = append(scopes, "service_keys:read")
		if stepChanges(step) {
			scopes = append(scopes, "service_keys:write")
		}
	case "create_provider", "provider_check", "provider_metadata", "provider_secret":
		scopes = append(scopes, "provider_credentials:read")
		if stepChanges(step) {
			scopes = append(scopes, "provider_credentials:write")
		}

	case "create_key", "key_policy", "key_credit", "key_overrun", "key_pricing", "key_cap", "revoke_key":
		scopes = append(scopes, "app_connections:read")
		if stepChanges(step) {
			switch step.Kind {
			case "revoke_key":
				scopes = append(scopes, "app_connections:revoke")
			case "key_cap":
				scopes = append(scopes, "caps:app_write")
			case "key_credit", "key_overrun", "key_pricing":
				scopes = append(scopes, "caps:org_write")
			default:
				scopes = append(scopes, "app_connections:write")
			}
			if step.Kind == "create_key" && (step.After.text("credit_mode") != "enforce_if_present" || step.After.text("overrun_policy") != "limit_output" || (step.After.has("product_user_charge_multiplier") && string(step.After["product_user_charge_multiplier"]) != "null")) {
				scopes = append(scopes, "caps:org_write")
			}
		}
	case "create_router", "router_name", "router_members", "revoke_router":
		scopes = append(scopes, "routing_keys:read")
		if len(step.Members) > 0 {
			scopes = append(scopes, "app_connections:read")
		}
		if stepChanges(step) {
			scopes = append(scopes, "routing_keys:write")
		}
	}
	return scopes
}

func planScopes(plan Plan) []string {
	result := []string{}
	for _, step := range plan.Steps {
		for _, scope := range stepScopes(step) {
			if !slices.Contains(result, scope) {
				result = append(result, scope)
			}
		}
	}
	slices.Sort(result)
	return result
}
