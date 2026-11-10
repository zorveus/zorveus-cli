package provision

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

var operationID = regexp.MustCompile(`^[A-Za-z0-9]{20,64}$`)
var resourceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type execution struct {
	plan    Plan
	journal Journal
	remote  *gateway
	secrets SecretStore
	inputs  map[string]string
}

func (service *Service) Apply(ctx context.Context, origin, path string, accepted, allowDelete bool) (ApplyResult, error) {
	if !accepted {
		return ApplyResult{}, problem.New("acceptance_required", "Review the saved plan, then pass --yes to apply it.", 2)
	}
	var plan Plan
	exists, _, err := loadJSON(path, &plan)
	if err != nil {
		return ApplyResult{}, err
	}
	if !exists || plan.Version != 1 || !operationID.MatchString(plan.ID) || !filepath.IsAbs(plan.Config) {
		return ApplyResult{}, problem.New("invalid_plan", "Provide a valid saved plan created by zorveus plan.", 2)
	}
	if _, err := manifest.Parse(plan.Manifest); err != nil {
		return ApplyResult{}, problem.New("invalid_plan", "The saved plan contains an invalid manifest.", 2)
	}
	if origin != "" && origin != plan.Target.Origin {
		return ApplyResult{}, conflict()
	}
	if !allowDelete && slices.ContainsFunc(plan.Steps, func(step Step) bool { return step.Destructive && stepChanges(step) }) {
		return ApplyResult{}, problem.New("destructive_acceptance_required", "This plan includes revocation, cap removal, or redirect removal. Pass --allow-delete as well as --yes after reviewing it.", 2)
	}
	unlock, err := lockConfig(ctx, plan.Config)
	if err != nil {
		return ApplyResult{}, err
	}
	defer unlock()
	remote := &gateway{ctx: ctx, service: service, target: plan.Target}
	if err := remote.identify(); err != nil {
		return ApplyResult{}, err
	}
	if err := remote.call(plan.Scopes, func(*api.Client, auth.Login) error { return nil }); err != nil {
		return ApplyResult{}, err
	}
	journal := Journal{Version: 1, Config: plan.Config, PlanDigest: digest(encoded(plan)), Steps: make([]Progress, len(plan.Steps))}
	resuming, _, err := loadJSON(journalPath(plan), &journal)
	if err != nil {
		return ApplyResult{}, err
	}
	if journal.Version != 1 || journal.Config != plan.Config || journal.PlanDigest != digest(encoded(plan)) || len(journal.Steps) != len(plan.Steps) {
		return ApplyResult{}, conflict()
	}
	current, err := os.ReadFile(plan.Config)
	if err != nil {
		return ApplyResult{}, storageError()
	}
	if journal.Complete {
		if digest(current) != journal.FinalDigest {
			return ApplyResult{}, conflict()
		}
		if err := service.rememberCredentials(plan, journal, true); err != nil {
			return ApplyResult{}, err
		}
		return applyResult(plan, journal), nil
	}
	if !resuming {
		if err := requireFinishedApply(plan.Config); err != nil {
			return ApplyResult{}, err
		}
		fresh, err := service.buildFrom(ctx, plan.Target.Origin, plan.Config, plan.Source, plan.KeepNames)
		if err != nil {
			return ApplyResult{}, err
		}
		if !samePlan(plan, fresh) {
			return ApplyResult{}, conflict()
		}
	} else if digest(current) != plan.ConfigDigest && journal.FinalDigest != digest(current) {
		return ApplyResult{}, problem.New("config_changed", "The manifest changed during apply. Preserve your edits and inspect the local apply journal for returned IDs before creating a new plan.", 4)
	}
	if resuming && journal.FinalDigest == "" {
		var state State
		_, data, err := loadJSON(statePath(plan.Config), &state)
		if err != nil {
			return ApplyResult{}, err
		}
		if digest(data) != plan.StateDigest {
			return ApplyResult{}, conflict()
		}
	}
	inputs, err := readSecretInputs(plan)
	if err != nil {
		return ApplyResult{}, err
	}
	secrets := service.secretStore()
	if slices.ContainsFunc(plan.Steps, stepUsesSecretStore) {
		if err := secrets.Check(plan.ID); err != nil {
			return ApplyResult{}, err
		}
	}
	run := &execution{plan: plan, journal: journal, remote: remote, secrets: secrets, inputs: inputs}
	if err := run.prepareProviderInputs(); err != nil {
		return ApplyResult{}, err
	}
	if err := run.save(); err != nil {
		return ApplyResult{}, err
	}
	for index, step := range plan.Steps {
		if err := ctx.Err(); err != nil {
			return ApplyResult{}, incomplete()
		}
		if err := run.step(index, step); err != nil {
			failure := problem.Safe(err)
			if failure.ExitCode == 3 || failure.ExitCode == 4 || failure.ExitCode == 5 {
				return ApplyResult{}, err
			}
			if run.journal.Steps[index].Started {
				return ApplyResult{}, problem.New("apply_incomplete", failure.Message+" Apply progress is saved in the local journal; retry the same plan.", 5)
			}
			return ApplyResult{}, err
		}
	}
	if err := service.rememberCredentials(plan, run.journal, false); err != nil {
		return ApplyResult{}, err
	}
	if err := run.finish(); err != nil {
		return ApplyResult{}, err
	}
	return applyResult(plan, run.journal), nil
}

func samePlan(left, right Plan) bool {
	if left.Source != right.Source || left.SourceDigest != right.SourceDigest || left.KeepNames != right.KeepNames {
		return false
	}
	var leftManifest, rightManifest Fields
	if json.Unmarshal(left.Manifest, &leftManifest) != nil || json.Unmarshal(right.Manifest, &rightManifest) != nil {
		return false
	}
	if left.Config != right.Config || !sameFields(leftManifest, rightManifest) {
		return false
	}
	if left.Target != right.Target || left.ConfigDigest != right.ConfigDigest || left.StateDigest != right.StateDigest || !reflect.DeepEqual(left.Bindings, right.Bindings) || !reflect.DeepEqual(left.Scopes, right.Scopes) || len(left.Steps) != len(right.Steps) {
		return false
	}
	for index, step := range left.Steps {
		other := right.Steps[index]
		if step.Kind != other.Kind || step.Ref != other.Ref || step.ID != other.ID || step.CapID != other.CapID || step.Destructive != other.Destructive || step.SecretEnv != other.SecretEnv || step.SecretKind != other.SecretKind || step.VersionID != other.VersionID || !reflect.DeepEqual(step.Members, other.Members) || !sameFields(step.Before, other.Before) || !sameFields(step.After, other.After) {
			return false
		}
	}
	return true
}

func (run *execution) save() error { return replaceFile(journalPath(run.plan), encoded(run.journal)) }
func (run *execution) id(ref string) string {
	if strings.HasPrefix(ref, "id:") {
		return strings.TrimPrefix(ref, "id:")
	}
	for index, step := range run.plan.Steps {
		if step.Ref == ref && run.journal.Steps[index].ID != "" {
			return run.journal.Steps[index].ID
		}
	}
	return run.plan.Bindings[ref]
}

func (run *execution) step(index int, step Step) error {
	progress := &run.journal.Steps[index]
	if step.Kind == "provider_secret" {
		return run.rotateProvider(index, step)
	}
	if strings.HasPrefix(step.Kind, "create_") {
		return run.create(index, step)
	}
	current, capID, err := run.current(step)
	if err != nil {
		return err
	}
	after := run.after(step)
	if sameFields(current, after) {
		progress.Done = true
		return run.save()
	}
	if progress.Done || !sameFields(current, step.Before) {
		return conflict()
	}
	if step.Kind == "key_cap" && step.CapID != "" && capID != step.CapID {
		return conflict()
	}
	progress.Started = true
	if err := run.save(); err != nil {
		return err
	}
	if err := run.update(step, capID); err != nil {
		return err
	}
	if err := run.verifyUpdate(step, after); err != nil {
		return err
	}
	progress.Done = true
	return run.save()
}

func (run *execution) after(step Step) Fields {
	result := Fields{}
	for name, value := range step.After {
		result[name] = value
	}
	if step.Kind == "router_members" || step.Kind == "create_router" {
		bindings := map[string]string{}
		for _, member := range step.Members {
			bindings[member.Ref] = run.id(member.Ref)
		}
		result.set("members", plannedMembers(step.Members, bindings))
	}
	return result
}

func (run *execution) current(step Step) (Fields, string, error) {
	id := run.id(step.Ref)
	if !resourceID.MatchString(id) {
		return nil, "", conflict()
	}
	if step.Kind == "create_service" || step.Kind == "service_check" || step.Kind == "service_metadata" {
		key, err := run.remote.serviceKey(id)
		if err != nil {
			return nil, "", err
		}
		if !serviceKeyActive(key, run.remote.service.Auth.Now()) {
			return nil, "", conflict()
		}
		return project(fields(key), step.After), "", nil
	}
	if step.Kind == "create_provider" || step.Kind == "provider_check" || step.Kind == "provider_metadata" {
		credential, err := run.remote.provider(id)
		if err != nil {
			return nil, "", err
		}
		if credential.Status != "active" {
			return nil, "", conflict()
		}
		return project(fields(credential), step.After), "", nil
	}
	if strings.HasPrefix(step.Kind, "app_redirect") && step.Kind != "app_redirects" {
		app, err := run.remote.app(id)
		if err != nil {
			return nil, "", err
		}
		present := slices.Contains(appFields(app).strings("redirect_uris"), step.After.text("redirect_uri"))
		return fields(map[string]any{"present": present, "redirect_uri": step.After.text("redirect_uri")}), "", nil
	}
	if step.Kind == "create_app" || step.Kind == "app_profile" || step.Kind == "app_models" || step.Kind == "app_redirects" {
		current, err := run.remote.app(id)
		if err != nil {
			return nil, "", err
		}
		return project(appFields(current), step.After), "", nil
	}
	appID := run.id("app")
	if !resourceID.MatchString(appID) {
		return nil, "", conflict()
	}
	if step.Kind == "create_router" || step.Kind == "router_name" || step.Kind == "router_members" || step.Kind == "revoke_router" {
		current, err := run.remote.router(appID, id)
		if err != nil {
			return nil, "", err
		}
		if step.Kind != "revoke_router" && current.Status != "active" {
			return nil, "", conflict()
		}
		return project(routerFields(current), step.After), "", nil
	}
	current, err := run.remote.key(appID, id)
	if err != nil {
		return nil, "", err
	}
	if step.Kind == "key_cap" {
		capID := ""
		if current.detail.Cap != nil {
			capID = current.detail.Cap.ID
		}
		return capFields(current), capID, nil
	}
	if step.Kind != "revoke_key" && (current.detail.Connection.Status != "active" || current.credential == nil || current.credential.Status != "active") {
		return nil, "", conflict()
	}
	return project(keyFields(current), step.After), "", nil
}

func project(current, desired Fields) Fields {
	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	return current.pick(names...)
}

func (run *execution) finish() error {
	var desired Manifest
	if err := json.Unmarshal(run.plan.Manifest, &desired); err != nil {
		return err
	}
	expectedAppID := run.plan.Bindings["app"]
	if run.journal.FinalDigest != "" {
		var saved State
		exists, _, err := loadJSON(statePath(run.plan.Config), &saved)
		if err != nil {
			return err
		}
		if exists && saved.AppID == run.id("app") {
			expectedAppID = saved.AppID
		}
	}
	state, _, err := readState(run.plan.Config, run.plan.Target, expectedAppID)
	if err != nil {
		return err
	}
	state.AppID = run.id("app")
	state.Target = run.plan.Target
	if desired.Keys != nil {
		state.Keys = map[string]string{}
		for _, key := range desired.Keys {
			state.Keys[key.text("routingAlias")] = run.id("key:" + key.text("routingAlias"))
		}
	}
	if desired.Routers != nil {
		state.Routers = []string{}
		for index := range desired.Routers {
			state.Routers = append(state.Routers, run.id(desired.resourceRef("routingKeys", index)))
		}
	}
	var document Fields
	_ = json.Unmarshal(run.plan.Manifest, &document)
	desired.App.set("id", state.AppID)
	document.set("app", desired.App)
	if desired.Keys != nil {
		for _, key := range desired.Keys {
			key.set("id", state.Keys[key.text("routingAlias")])
		}
		document.set("keys", desired.resourceValue("keys", desired.Keys))
	}
	if desired.Routers != nil {
		for index, router := range desired.Routers {
			router.set("id", state.Routers[index])
		}
		document.set("routingKeys", desired.resourceValue("routingKeys", desired.Routers))
	}
	if desired.ServiceKeys != nil {
		for index, key := range desired.ServiceKeys {
			key.set("id", run.id(desired.resourceRef("serviceKeys", index)))
		}
		document.set("serviceKeys", desired.resourceValue("serviceKeys", desired.ServiceKeys))
	}
	if desired.Providers != nil {
		for index, credential := range desired.Providers {
			credential.set("id", run.id(desired.resourceRef("providerCredentials", index)))
		}
		document.set("providerCredentials", desired.resourceValue("providerCredentials", desired.Providers))
	}
	final := encoded(document)
	if _, err := manifest.Parse(final); err != nil {
		return err
	}
	current, err := os.ReadFile(run.plan.Config)
	if err != nil {
		return storageError()
	}
	if digest(current) != run.plan.ConfigDigest && digest(current) != run.journal.FinalDigest {
		return problem.New("config_changed", "Remote changes completed, but the manifest changed. Your edits were preserved. Inspect the local apply journal for returned IDs before creating a new plan.", 4)
	}
	run.journal.FinalDigest = digest(final)
	if err := run.save(); err != nil {
		return err
	}
	if err := replaceFile(run.plan.Config, final); err != nil {
		return err
	}
	if err := replaceFile(statePath(run.plan.Config), encoded(state)); err != nil {
		return err
	}
	run.journal.Complete = true
	return run.save()
}

func applyResult(plan Plan, journal Journal) ApplyResult {
	result := ApplyResult{Operation: plan.ID, Config: plan.Config, CredentialAccounts: []string{}}
	for index, progress := range journal.Steps {
		if progress.Done {
			result.Completed++
		}
		if plan.Steps[index].Ref == "app" && progress.ID != "" {
			result.AppID = progress.ID
		}
		if stepHasCredential(plan.Steps[index]) {
			result.CredentialAccounts = append(result.CredentialAccounts, plan.ID+"/"+plan.Steps[index].Ref)
		}
	}
	if result.AppID == "" {
		result.AppID = plan.Bindings["app"]
	}
	return result
}

func secretMissing(err error) bool { return errors.Is(err, ErrSecretMissing) }
