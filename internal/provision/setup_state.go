package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

// CheckSetupState catches a reused filename before the wizard collects answers.
// Planning and apply still validate the target again under the config lock.
func CheckSetupState(config string, target Target, appID string) error {
	config, err := filepath.Abs(config)
	if err != nil {
		return storageError()
	}
	info, err := os.Lstat(directory(config))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return storageError()
	}
	if err := requireFinishedApply(config); err != nil {
		return err
	}
	_, _, err = readState(config, target, appID)
	return err
}

type SetupConfiguration struct {
	Config string
	Name   string
	AppID  string
}

// Discover only manifests whose IDs match saved state in this folder and target.
// Display names alone are never enough to identify an existing app.
func FindSetupConfigurations(config string, target Target) ([]SetupConfiguration, error) {
	absolute, err := filepath.Abs(config)
	if err != nil {
		return nil, storageError()
	}
	entries, err := os.ReadDir(directory(absolute))
	if err != nil {
		return nil, storageError()
	}
	result := []SetupConfiguration{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".state.json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".state.json")
		if name == "" || name == filepath.Base(absolute) {
			continue
		}
		var state State
		if _, _, err := loadJSON(filepath.Join(directory(absolute), entry.Name()), &state); err != nil {
			continue
		}
		if state.Version != 1 || state.Target.Origin != target.Origin || state.Target.Org != target.Org || !resourceID.MatchString(state.AppID) {
			continue
		}
		candidate := filepath.Join(filepath.Dir(absolute), name)
		desired, _, _, err := loadManifest(candidate)
		if err != nil || desired.App.text("id") != state.AppID {
			continue
		}
		result = append(result, SetupConfiguration{Config: filepath.Join(filepath.Dir(config), name), Name: desired.App.text("name"), AppID: state.AppID})
	}
	return result, nil
}

// Back up only the conflicting state. Completed journals stay in place so an old
// saved creation plan cannot be mistaken for a plan that has never been applied.
func ArchiveSetupState(ctx context.Context, config string, target Target, expectedAppID string) (string, error) {
	config, err := filepath.Abs(config)
	if err != nil {
		return "", storageError()
	}
	unlock, err := lockConfig(ctx, config)
	if err != nil {
		return "", err
	}
	defer unlock()
	appID := ""
	if _, err := os.Lstat(config); err == nil {
		desired, _, _, err := loadManifest(config)
		if err != nil {
			return "", err
		}
		appID = desired.App.text("id")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", storageError()
	}
	if appID != expectedAppID {
		return "", setupStateChanged()
	}
	err = CheckSetupState(config, target, appID)
	if err == nil {
		return "", setupStateChanged()
	}
	if problem.Safe(err).Code != "state_target_mismatch" {
		return "", err
	}
	backup, err := os.CreateTemp(directory(config), filepath.Base(config)+".state.backup-*.json")
	if err != nil {
		return "", storageError()
	}
	path := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(path)
		return "", storageError()
	}
	if err := os.Rename(statePath(config), path); err != nil {
		_ = os.Remove(path)
		return "", storageError()
	}
	return path, nil
}

func setupStateChanged() error {
	return problem.New("setup_state_changed", "This setup changed while you were choosing an app. Run setup again to review it.", 4)
}
