package provision

import (
	"net/http"
	"net/url"
	"os"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func (run *execution) readDestroyResource(step Step) (Fields, string, error) {
	if !resourceID.MatchString(step.ID) {
		return nil, "", conflict()
	}
	status := ""
	var err error
	switch step.Kind {
	case "delete_app":
		err = run.remote.call([]string{"apps:read"}, func(client *api.Client, login auth.Login) error {
			app, readErr := client.GetApp(run.remote.ctx, login.APIOrigin, login.Organization, step.ID, login.AccessToken)
			status = app.Status
			return readErr
		})
	case "revoke_service":
		key, readErr := run.remote.serviceKey(step.ID)
		status, err = key.Status, readErr
	case "delete_provider":
		credential, readErr := run.remote.provider(step.ID)
		status, err = credential.Status, readErr
	case "revoke_key", "revoke_router", "key_cap":
		// A resource's explicit ID remains unambiguous even if aliases changed.
		step.Ref = "id:" + step.ID
		return run.current(step)
	default:
		return nil, "", conflict()
	}
	return fields(map[string]string{"status": status}), "", err
}

func (run *execution) destroyStep(index int, step Step) error {
	progress := &run.journal.Steps[index]
	if progress.Done {
		return nil
	}
	current, capID, err := run.readDestroyResource(step)
	if err != nil {
		if step.Kind == "delete_app" && progress.Started && problem.Safe(err).Code == "access_denied" {
			return problem.New("destroy_app_outcome_unknown", "App deletion was attempted, but the API no longer permits its lookup. Deletion cannot be distinguished from lost access. Keep the destroy plan and journal; backend confirmation is required before marking this step complete.", 5)
		}
		if problem.Safe(err).Code != "resource_not_found" {
			return err
		}
		progress.Done = true
		return run.save()
	}
	if sameFields(current, step.After) {
		progress.Done = true
		return run.save()
	}
	if !sameFields(current, step.Before) || step.Kind == "key_cap" && capID != step.CapID {
		return conflict()
	}
	progress.Started = true
	if err := run.save(); err != nil {
		return err
	}
	if err := run.sendDestroyRequest(step); err != nil {
		return err
	}
	if step.Kind != "delete_app" && step.Kind != "delete_provider" && step.Kind != "revoke_service" {
		actual, _, err := run.readDestroyResource(step)
		if err != nil && problem.Safe(err).Code != "resource_not_found" {
			return err
		}
		if err == nil && !sameFields(actual, step.After) {
			return problem.New("destroy_incomplete", "The API has not confirmed this removal. Keep the saved destroy plan and retry it.", 5)
		}
	}
	progress.Done = true
	return run.save()
}

func (run *execution) sendDestroyRequest(step Step) error {
	return run.remote.call(stepScopes(step), func(client *api.Client, login auth.Login) error {
		ctx, origin, organization, token := run.remote.ctx, login.APIOrigin, login.Organization, login.AccessToken
		switch step.Kind {
		case "delete_app":
			// App deletion returns an explicit confirmation; its GET route then becomes inaccessible.
			return client.DeleteApp(ctx, origin, organization, step.ID, token)
		case "delete_provider":
			return client.DeleteProviderCredential(ctx, origin, organization, step.ID, token)
		case "revoke_service":
			_, err := client.RevokeServiceKey(ctx, origin, organization, step.ID, token)
			return err
		case "revoke_router":
			path := "/orgs/" + organization + "/apps/" + run.id("app") + "/routing-keys/" + step.ID + "/revoke"
			return client.WriteJSON(ctx, origin, http.MethodPost, path, token, nil, nil)
		case "revoke_key":
			path := "/app-connections/" + step.ID + "/revoke?" + url.Values{"org_id": {organization}}.Encode()
			return client.WriteJSON(ctx, origin, http.MethodPost, path, token, nil, nil)
		case "key_cap":
			if !resourceID.MatchString(step.CapID) {
				return conflict()
			}
			return client.WriteJSON(ctx, origin, http.MethodDelete, "/caps/"+step.CapID, token, nil, nil)
		default:
			return conflict()
		}
	})
}

func (run *execution) finishDestroy() error {
	current, err := os.ReadFile(run.plan.Config)
	if err != nil {
		return storageError()
	}
	if digest(current) != run.plan.ConfigDigest && digest(current) != run.journal.FinalDigest {
		return problem.New("config_changed", "Resources were removed, but the manifest changed during destroy. Your edits were preserved. Restore the reviewed manifest before resuming its saved destroy plan.", 4)
	}
	state, _, err := readState(run.plan.Config, run.plan.Target, run.id("app"))
	if err != nil {
		return err
	}
	state.AppID, state.Target, state.Destroyed = run.id("app"), run.plan.Target, true
	state.DestroyedIDs = removedResourceIDs(run.plan)
	run.journal.FinalDigest = digest(current)
	if workspace := run.remote.service.workspace; workspace != nil && state.AppID != "" {
		workspace.snapshot.State, workspace.snapshot.Manifest = &state, run.plan.Manifest
		workspace.snapshot.ProviderFingerprints = nil
	}
	if err := run.save(); err != nil {
		return err
	}
	if state.AppID != "" {
		if err := replaceFile(statePath(run.plan.Config), encoded(state)); err != nil {
			return err
		}
	}
	run.journal.Complete = true
	return run.save()
}
