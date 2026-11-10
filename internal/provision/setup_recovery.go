package provision

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

// UseSetupConfiguration restores an applied setup at the requested filename.
// Both local files are backed up before replacing either one. Backend resources
// and the source configuration are never changed here.
func UseSetupConfiguration(ctx context.Context, selected SetupConfiguration, destination string, target Target, expected []byte) (string, error) {
	source, err := filepath.Abs(selected.Config)
	if err != nil {
		return "", storageError()
	}
	destination, err = filepath.Abs(destination)
	if err != nil || source == destination {
		return "", setupStateChanged()
	}
	paths := []string{source, destination}
	slices.Sort(paths)
	for _, path := range paths {
		unlock, err := lockConfig(ctx, path)
		if err != nil {
			return "", err
		}
		defer unlock()
	}
	desired, _, canonical, err := loadManifest(source)
	if err != nil {
		return "", err
	}
	if desired.App.text("id") != selected.AppID || selected.AppID == "" {
		return "", setupStateChanged()
	}
	if err := CheckSetupState(source, target, selected.AppID); err != nil {
		return "", err
	}
	var sourceState State
	found, sourceStateBytes, err := loadJSON(statePath(source), &sourceState)
	if err != nil || !found || sourceState.AppID != selected.AppID || sourceState.Target.Origin != target.Origin || sourceState.Target.Org != target.Org {
		return "", setupStateChanged()
	}
	var previous []byte
	appID := ""
	if _, err := os.Lstat(destination); err == nil {
		current, original, currentCanonical, err := loadManifest(destination)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(expected, currentCanonical) {
			return "", setupStateChanged()
		}
		previous, appID = original, current.App.text("id")
	} else if !errors.Is(err, os.ErrNotExist) || expected != nil {
		return "", setupStateChanged()
	}
	err = CheckSetupState(destination, target, appID)
	if err == nil {
		return "", setupStateChanged()
	}
	if problem.Safe(err).Code != "state_target_mismatch" {
		return "", err
	}
	var oldState State
	oldExists, oldStateBytes, err := loadJSON(statePath(destination), &oldState)
	if err != nil || !oldExists {
		return "", setupStateChanged()
	}
	backup, err := os.MkdirTemp(directory(destination), "setup-backup-")
	if err != nil {
		return "", storageError()
	}
	if previous != nil {
		if err := writeNew(filepath.Join(backup, filepath.Base(destination)), previous); err != nil {
			return "", err
		}
	}
	if err := writeNew(filepath.Join(backup, filepath.Base(statePath(destination))), oldStateBytes); err != nil {
		return "", err
	}
	if err := replaceFile(destination, canonical); err != nil {
		return "", err
	}
	if err := replaceFile(statePath(destination), sourceStateBytes); err != nil {
		// Keep the previous pair together if saving the restored state fails.
		var restoreErr error
		if previous == nil {
			restoreErr = os.Remove(destination)
		} else {
			restoreErr = replaceFile(destination, previous)
		}
		if restoreErr != nil {
			return "", problem.New("setup_recovery_incomplete", "Could not finish restoring setup. The previous files are preserved in "+backup+".", 1)
		}
		return "", err
	}
	return backup, nil
}
