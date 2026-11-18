package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/statestore"
)

type savedOperation struct {
	Plan    Plan    `json:"plan"`
	Journal Journal `json:"journal"`
}

type durableSnapshot struct {
	Version              int                                  `json:"version"`
	Backend              statestore.Config                    `json:"backend"`
	State                *State                               `json:"state,omitempty"`
	Manifest             json.RawMessage                      `json:"applied_manifest,omitempty"`
	Operation            *savedOperation                      `json:"operation,omitempty"`
	InputFingerprints    map[string]string                    `json:"input_fingerprints,omitempty"`
	ProviderFingerprints map[string]providerFingerprintRecord `json:"provider_fingerprints,omitempty"`
}

type providerFingerprintRecord struct {
	VersionID   string `json:"version_id"`
	Fingerprint string `json:"fingerprint"`
}

type durableWorkspace struct {
	config   statestore.Config
	session  *statestore.Session
	snapshot durableSnapshot
}

func (service *Service) backendConfig(config string, planned *statestore.Config) (*statestore.Config, error) {
	var selected *statestore.Config
	if data, err := os.ReadFile(config); err == nil {
		var document Fields
		if json.Unmarshal(data, &document) != nil {
			return nil, storageError()
		}
		if value := document["_state"]; value != nil {
			parsed, err := statestore.ParseConfig(value)
			if err != nil {
				return nil, err
			}
			selected = &parsed
		}
	} else if !os.IsNotExist(err) {
		return nil, storageError()
	}
	for _, candidate := range []*statestore.Config{service.StateConfig, planned} {
		if candidate == nil {
			continue
		}
		if err := candidate.Validate(); err != nil {
			return nil, err
		}
		if selected != nil && *selected != *candidate {
			return nil, problem.New("state_backend_mismatch", "The manifest, saved plan, and --state-config must select the same durable backend. Review the bucket, key, endpoint, and region.", 4)
		}
		copy := *candidate
		selected = &copy
	}
	if selected == nil {
		receipt := filepath.Join(directory(config), filepath.Base(config)+".backend.json")
		if _, err := os.Lstat(receipt); err == nil {
			return nil, problem.New("state_backend_required", "This manifest is bound to durable state. Restore its _state block or supply --state-config. The CLI will not fall back to local state.", 4)
		} else if !os.IsNotExist(err) {
			return nil, storageError()
		}
	}
	return selected, nil
}

func (service *Service) backend(ctx context.Context, config statestore.Config) (statestore.Backend, error) {
	if service.BackendFactory != nil {
		return service.BackendFactory(ctx, config)
	}
	return statestore.NewBackend(ctx, config)
}

// Callers hold the local manifest lock while opening and using this workspace.
func (service *Service) openWorkspace(ctx context.Context, config string, planned *statestore.Config) (*Service, func() error, error) {
	selected, err := service.backendConfig(config, planned)
	if err != nil {
		return nil, nil, err
	}
	if selected == nil {
		return service, func() error { return nil }, nil
	}
	backend, err := service.backend(ctx, *selected)
	if err != nil {
		return nil, nil, err
	}
	session, err := statestore.Acquire(ctx, backend)
	if err != nil {
		return nil, nil, err
	}
	workspace := &durableWorkspace{config: *selected, session: session}
	if err := decodeSnapshot(session.Document, &workspace.snapshot); err != nil {
		_ = session.Close()
		return nil, nil, err
	}
	if !selected.SameLocation(workspace.snapshot.Backend) {
		_ = session.Close()
		return nil, nil, problem.New("state_backend_mismatch", "This state snapshot belongs to a different bucket, key, or endpoint. Restore its original backend location instead of copying an active deployment's state to a new key.", 4)
	}
	if err := workspace.hydrate(config, session.Document.ID); err != nil {
		_ = session.Close()
		return nil, nil, err
	}
	scoped := *service
	scoped.workspace = workspace
	return &scoped, session.Close, nil
}

func closeWorkspace(close func() error, operationErr *error) {
	if err := close(); err != nil {
		if *operationErr == nil {
			*operationErr = err
			return
		}
		failure := problem.Safe(err)
		*operationErr = problem.New(failure.Code, problem.Safe(*operationErr).Message+" "+failure.Message, failure.ExitCode)
	}
}

func (workspace *durableWorkspace) hydrate(config, stateID string) error {
	// Check explicit IDs before replacing any local mirror of remote state.
	data, err := os.ReadFile(config)
	if err == nil {
		if _, err := workspace.resolveIDs(data); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return storageError()
	}
	var receipt struct {
		ID string `json:"id"`
	}
	receiptPath := filepath.Join(directory(config), filepath.Base(config)+".backend.json")
	if exists, _, err := loadJSON(receiptPath, &receipt); err != nil {
		return err
	} else if exists && receipt.ID != stateID {
		return problem.New("state_backend_mismatch", "This manifest's local state is bound to a different durable state ID. Use a separate project directory or restore the original backend.", 4)
	}
	var local State
	if exists, localBytes, err := loadJSON(statePath(config), &local); err != nil {
		return err
	} else if exists {
		remote := workspace.snapshot.State
		if remote == nil || local.Target.Origin != remote.Target.Origin || local.Target.Org != remote.Target.Org {
			return problem.New("state_backend_mismatch", "Local tracking belongs to a different app or organization than this durable state. Use a separate project directory or initialize the backend with --copy-local after reviewing it.", 4)
		}
		if local.AppID != remote.AppID && (!workspace.finalizingApply(data) || digest(localBytes) != workspace.snapshot.Operation.Plan.StateDigest) {
			return problem.New("state_backend_mismatch", "Local tracking belongs to a different app than this durable state. Restore the matching project tracking before continuing.", 4)
		}
	}
	entries, err := os.ReadDir(directory(config))
	if err != nil {
		return storageError()
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".apply.json") {
			continue
		}
		path := filepath.Join(directory(config), entry.Name())
		var journal Journal
		if _, _, err := loadJSON(path, &journal); err != nil {
			return err
		}
		if planFilename(journal.Config) != filepath.Base(config) {
			continue
		}
		if workspace.snapshot.Operation != nil && entry.Name() == workspace.snapshot.Operation.Plan.ID+".apply.json" {
			continue
		}
		if err := os.Rename(path, path+".local-backup-"+newID()); err != nil {
			return storageError()
		}
	}
	if workspace.snapshot.State != nil {
		if err := replaceFile(statePath(config), encoded(workspace.snapshot.State)); err != nil {
			return err
		}
	}
	if operation := workspace.snapshot.Operation; operation != nil {
		plan := relocatePlan(operation.Plan, config)
		journal := operation.Journal
		journal.Config = config
		if journal.PlanDigest != planDigest(plan) {
			return problem.New("invalid_remote_state", "The saved apply journal does not match its durable plan. Restore a known-good state version.", 4)
		}
		if err := replaceFile(journalPath(plan), encoded(journal)); err != nil {
			return err
		}
	}
	return replaceFile(receiptPath, encoded(map[string]string{"id": stateID}))
}

// Only IDs are filled from the last successful apply. Current settings and removals win.
func (workspace *durableWorkspace) resolveIDs(data []byte) ([]byte, error) {
	if _, err := manifest.Parse(data); err != nil {
		return nil, err
	}
	if workspace.finalizingApply(data) {
		return workspace.snapshot.Manifest, nil
	}
	if state := workspace.snapshot.State; state != nil && state.Destroyed {
		var document Fields
		_ = json.Unmarshal(data, &document)
		if org := document.text("organizationId"); org != "" && org != state.Target.Org {
			return nil, problem.New("organization_mismatch", "The manifest's organization differs from the destroyed deployment's organization.", 4)
		}
		document.set("organizationId", state.Target.Org)
		// A destroyed snapshot must not restore its former IDs into the desired configuration.
		return encoded(document), nil
	}
	if len(workspace.snapshot.Manifest) == 0 {
		return data, nil
	}
	var desired, applied map[string]json.RawMessage
	if json.Unmarshal(data, &desired) != nil || json.Unmarshal(workspace.snapshot.Manifest, &applied) != nil {
		return nil, storageError()
	}
	var currentApp, savedApp Fields
	_ = json.Unmarshal(desired["app"], &currentApp)
	_ = json.Unmarshal(applied["app"], &savedApp)
	if err := bindID(currentApp, savedApp); err != nil {
		return nil, err
	}
	desired["app"] = encoded(currentApp)
	if value := applied["organizationId"]; value != nil {
		if current := desired["organizationId"]; current != nil && !bytes.Equal(bytes.TrimSpace(current), bytes.TrimSpace(value)) {
			return nil, problem.New("organization_mismatch", "This manifest's organization differs from the durable state's organization.", 4)
		}
		desired["organizationId"] = value
	}
	for _, section := range []string{"keys", "routingKeys", "serviceKeys", "providerCredentials"} {
		if desired[section] == nil {
			continue
		}
		var current, previous map[string]Fields
		if json.Unmarshal(desired[section], &current) != nil {
			return nil, storageError()
		}
		_ = json.Unmarshal(applied[section], &previous)
		for name, resource := range current {
			if err := bindID(resource, previous[name]); err != nil {
				return nil, err
			}
		}
		desired[section] = encoded(current)
	}
	resolved := encoded(desired)
	if _, err := manifest.Parse(resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}

func decodeSnapshot(document statestore.Document, snapshot *durableSnapshot) error {
	invalid := func() error {
		return problem.New("invalid_remote_state", "The durable provisioning snapshot is invalid. Restore a known-good object version before continuing.", 4)
	}
	if json.Unmarshal(document.Data, snapshot) != nil || snapshot.Version != 1 || snapshot.Backend.Validate() != nil {
		return invalid()
	}
	if snapshot.State != nil {
		state := snapshot.State
		if state.Version != 1 || !resourceID.MatchString(state.AppID) || state.Target.Origin == "" || state.Target.Org == "" {
			return invalid()
		}
		applied, err := manifest.Parse(snapshot.Manifest)
		if err != nil || applied.AppID() != state.AppID || applied.OrganizationID() != state.Target.Org {
			return invalid()
		}
	} else if len(snapshot.Manifest) != 0 {
		return invalid()
	}
	if operation := snapshot.Operation; operation != nil {
		plan, journal := operation.Plan, operation.Journal
		if plan.Version != 1 || !operationID.MatchString(plan.ID) || plan.RemoteStateID != document.ID || !absolutePlanPath(plan.Config) || journal.Version != 1 || journal.Config != plan.Config || journal.PlanDigest != planDigest(plan) || len(journal.Steps) != len(plan.Steps) {
			return invalid()
		}
		if _, err := manifest.Parse(plan.Manifest); err != nil {
			return invalid()
		}
	}
	return nil
}

func bindID(current, previous Fields) error {
	if previous.text("id") == "" {
		return nil
	}
	if current.text("id") != "" && current.text("id") != previous.text("id") {
		return problem.New("state_resource_mismatch", "A manifest resource ID differs from its durable state. Keep the saved ID or use another resource reference and review the resulting changes.", 4)
	}
	current.set("id", previous.text("id"))
	return nil
}

func (service *Service) resolvedManifest(path string) (Manifest, []byte, []byte, error) {
	desired, original, canonical, err := loadManifest(path)
	if err != nil || service.workspace == nil {
		return desired, original, canonical, err
	}
	if service.workspace.finalizingApply(original) {
		canonical = service.workspace.snapshot.Manifest
	} else {
		canonical, err = service.workspace.resolveIDs(canonical)
	}
	if err != nil {
		return Manifest{}, nil, nil, err
	}
	if err := json.Unmarshal(canonical, &desired); err != nil {
		return Manifest{}, nil, nil, err
	}
	return desired, original, canonical, nil
}

func (workspace *durableWorkspace) finalizingApply(data []byte) bool {
	operation := workspace.snapshot.Operation
	if operation == nil || operation.Plan.Destroy || operation.Journal.Complete || operation.Journal.FinalDigest == "" {
		return false
	}
	current := digest(data)
	var final Fields
	if json.Unmarshal(workspace.snapshot.Manifest, &final) != nil {
		return false
	}
	// Embedding JSON in the S3 snapshot changes indentation, not its saved contents.
	return (current == operation.Plan.ConfigDigest || current == operation.Journal.FinalDigest) && digest(encoded(final)) == operation.Journal.FinalDigest
}

func planDigest(plan Plan) string {
	if plan.RemoteStateID != "" {
		// Paths change between runners; content digests and the state ID bind the plan.
		plan.Config, plan.Source = "", ""
	}
	return digest(encoded(plan))
}

func relocatePlan(plan Plan, config string) Plan {
	if plan.Source != "" {
		original := filepath.FromSlash(path.Dir(strings.ReplaceAll(plan.Config, "\\", "/")))
		source := filepath.FromSlash(strings.ReplaceAll(plan.Source, "\\", "/"))
		relative, err := filepath.Rel(original, source)
		if err == nil {
			plan.Source = filepath.Join(filepath.Dir(config), relative)
		}
	}
	plan.Config = config
	return plan
}

func (workspace *durableWorkspace) checkpoint(ctx context.Context, plan Plan, journal Journal) error {
	workspace.snapshot.Operation = &savedOperation{Plan: plan, Journal: journal}
	return workspace.session.Save(ctx, encoded(workspace.snapshot))
}
