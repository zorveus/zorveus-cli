package provision

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/statestore"
)

type StateInfo struct {
	Destroyed          bool              `json:"destroyed,omitempty"`
	Backend            statestore.Config `json:"backend"`
	ID                 string            `json:"id"`
	Revision           uint64            `json:"revision"`
	Lock               *statestore.Lock  `json:"lock,omitempty"`
	AppID              string            `json:"app_id,omitempty"`
	Operation          string            `json:"operation,omitempty"`
	ApplyComplete      bool              `json:"apply_complete"`
	BackupPath         string            `json:"backup_path,omitempty"`
	AlreadyInitialized bool              `json:"already_initialized,omitempty"`
}

func (service *Service) requiredBackend(ctx context.Context, config string) (statestore.Backend, statestore.Config, error) {
	selected, err := service.backendConfig(config, nil)
	if err != nil {
		return nil, statestore.Config{}, err
	}
	if selected == nil {
		return nil, statestore.Config{}, problem.New("state_backend_required", "Add an _state backend configuration to the manifest or supply --state-config.", 2)
	}
	backend, err := service.backend(ctx, *selected)
	return backend, *selected, err
}

func (service *Service) InitializeState(ctx context.Context, config string, copyLocal, force bool) (StateInfo, error) {
	if copyLocal && force {
		return StateInfo{}, problem.New("invalid_arguments", "Choose --copy-local to keep managing the same app, or --force to back up local tracking and start fresh.", 2)
	}
	config, err := filepath.Abs(config)
	if err != nil {
		return StateInfo{}, storageError()
	}
	unlock, err := lockConfig(ctx, config)
	if err != nil {
		return StateInfo{}, err
	}
	defer unlock()
	desired, _, canonical, err := loadManifest(config)
	if err != nil {
		return StateInfo{}, err
	}
	backend, selected, err := service.requiredBackend(ctx, config)
	if err != nil {
		return StateInfo{}, err
	}
	receipt := filepath.Join(directory(config), filepath.Base(config)+".backend.json")
	_, receiptErr := os.Lstat(receipt)
	if receiptErr != nil && !os.IsNotExist(receiptErr) {
		return StateInfo{}, storageError()
	}
	document, _, err := statestore.Read(ctx, backend)
	if err == nil {
		return reuseInitializedState(config, selected, document)
	}
	if problem.Safe(err).Code != "remote_state_missing" {
		return StateInfo{}, err
	}
	if receiptErr == nil {
		return StateInfo{}, problem.New("remote_state_missing", "This manifest was previously bound to durable state, but its S3 object is missing. Check the configured backend or restore that object. Initialization will not create an empty replacement.", 4)
	}
	var local State
	exists := false
	if !force {
		if err := requireFinishedApply(config); err != nil {
			return StateInfo{}, err
		}
		exists, _, err = loadJSON(statePath(config), &local)
		if err != nil {
			return StateInfo{}, err
		}
		if exists && !copyLocal {
			return StateInfo{}, problem.New("local_state_exists", "This manifest already has local tracking. Use --copy-local to keep managing the same app, or --force to back up its local tracking and start fresh. Existing durable state is never overwritten.", 4)
		}
	}
	snapshot := durableSnapshot{Version: 1, Backend: selected}
	if copyLocal {
		if !exists {
			return StateInfo{}, problem.New("local_state_missing", "There is no local state to copy for this manifest.", 4)
		}
		if _, _, err := readState(config, local.Target, desired.App.text("id")); err != nil {
			return StateInfo{}, err
		}
		if desired.OrganizationID != "" && desired.OrganizationID != local.Target.Org {
			return StateInfo{}, conflict()
		}
		var document Fields
		_ = json.Unmarshal(canonical, &document)
		document.set("organizationId", local.Target.Org)
		snapshot.State, snapshot.Manifest = &local, encoded(document)
	}
	var backup *localStateBackup
	if force {
		backup, err = archiveLocalTracking(config)
		if err != nil {
			return StateInfo{}, err
		}
	}
	document, err = statestore.Initialize(ctx, backend, encoded(snapshot))
	if err != nil {
		if backup != nil {
			if restoreErr := backup.restore(); restoreErr != nil {
				return StateInfo{}, restoreErr
			}
		}
		// Another checkout may have initialized the object after our first read.
		if problem.Safe(err).Code == "remote_state_conflict" {
			document, _, err := statestore.Read(ctx, backend)
			if err != nil {
				return StateInfo{}, err
			}
			return reuseInitializedState(config, selected, document)
		}
		return StateInfo{}, err
	}
	if err := replaceFile(receipt, encoded(map[string]string{"id": document.ID})); err != nil {
		message := "Durable state was initialized, but its local binding could not be saved. Run state inspect before continuing."
		if backup != nil {
			message += " Previous local tracking is backed up in " + backup.directory + "."
		}
		return StateInfo{}, problem.New("state_binding_unavailable", message, 5)
	}
	result := stateInfo(selected, document, snapshot)
	if backup != nil {
		result.BackupPath = backup.directory
	}
	return result, nil
}

func reuseInitializedState(config string, selected statestore.Config, document statestore.Document) (StateInfo, error) {
	workspace := &durableWorkspace{config: selected}
	if err := decodeSnapshot(document, &workspace.snapshot); err != nil {
		return StateInfo{}, err
	}
	if !selected.SameLocation(workspace.snapshot.Backend) {
		return StateInfo{}, problem.New("state_backend_mismatch", "The existing state belongs to a different bucket, key, or endpoint. Restore its original backend location.", 4)
	}
	// Repeated init restores local mirrors without changing remote state or its lock.
	if err := workspace.hydrate(config, document.ID); err != nil {
		return StateInfo{}, err
	}
	result := stateInfo(selected, document, workspace.snapshot)
	result.AlreadyInitialized = true
	return result, nil
}

func (service *Service) InspectState(ctx context.Context, config string) (StateInfo, error) {
	backend, selected, err := service.requiredBackend(ctx, config)
	if err != nil {
		return StateInfo{}, err
	}
	document, _, err := statestore.Read(ctx, backend)
	if err != nil {
		return StateInfo{}, err
	}
	var snapshot durableSnapshot
	if err := decodeSnapshot(document, &snapshot); err != nil {
		return StateInfo{}, err
	}
	return stateInfo(selected, document, snapshot), nil
}

func (service *Service) UnlockState(ctx context.Context, config, lockID string, accepted bool) error {
	if !accepted {
		return problem.New("acceptance_required", "Confirm the previous deployment has stopped, then pass --yes with its exact --lock-id.", 2)
	}
	backend, _, err := service.requiredBackend(ctx, config)
	if err != nil {
		return err
	}
	return statestore.Unlock(ctx, backend, lockID)
}

func stateInfo(config statestore.Config, document statestore.Document, snapshot durableSnapshot) StateInfo {
	info := StateInfo{Backend: config, ID: document.ID, Revision: document.Revision, Lock: document.Lock}
	if snapshot.State != nil {
		info.AppID = snapshot.State.AppID
		info.Destroyed = snapshot.State.Destroyed
	}
	if snapshot.Operation != nil {
		info.Operation = snapshot.Operation.Plan.ID
		info.ApplyComplete = snapshot.Operation.Journal.Complete
	}
	return info
}

func (service *Service) Resume(ctx context.Context, origin, config string, accepted, allowDelete bool) (result ApplyResult, err error) {
	if !accepted {
		return ApplyResult{}, problem.New("acceptance_required", "Review the saved operation, then pass --yes to resume it.", 2)
	}
	config, err = filepath.Abs(config)
	if err != nil {
		return ApplyResult{}, storageError()
	}
	unlock, err := lockConfig(ctx, config)
	if err != nil {
		return ApplyResult{}, err
	}
	defer unlock()
	service, close, err := service.openWorkspace(ctx, config, nil)
	if err != nil {
		return ApplyResult{}, err
	}
	defer closeWorkspace(close, &err)
	if service.workspace == nil || service.workspace.snapshot.Operation == nil || service.workspace.snapshot.Operation.Journal.Complete {
		return ApplyResult{}, problem.New("no_pending_apply", "No unfinished apply is saved in this manifest's durable state.", 4)
	}
	plan := relocatePlan(service.workspace.snapshot.Operation.Plan, config)
	return service.applyPlan(ctx, origin, plan, allowDelete)
}

func (service *Service) PullState(ctx context.Context, config, output string) (StateInfo, error) {
	backend, selected, err := service.requiredBackend(ctx, config)
	if err != nil {
		return StateInfo{}, err
	}
	document, _, err := statestore.Read(ctx, backend)
	if err != nil {
		return StateInfo{}, err
	}
	var snapshot durableSnapshot
	if err := decodeSnapshot(document, &snapshot); err != nil {
		return StateInfo{}, err
	}
	if err := writeNew(output, encoded(document)); err != nil {
		return StateInfo{}, err
	}
	return stateInfo(selected, document, snapshot), nil
}
