package provision

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

type localStateBackup struct {
	directory string
	originals []string
}

// The caller holds the manifest lock. Other manifests keep their own tracking.
func archiveLocalTracking(config string) (*localStateBackup, error) {
	entries, err := os.ReadDir(directory(config))
	if err != nil {
		return nil, storageError()
	}
	var paths []string
	for _, entry := range entries {
		path := filepath.Join(directory(config), entry.Name())
		if path == statePath(config) {
			if !entry.Type().IsRegular() {
				return nil, storageError()
			}
			paths = append(paths, path)
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".apply.json") {
			continue
		}
		var journal Journal
		if _, _, err := loadJSON(path, &journal); err != nil {
			return nil, err
		}
		if planFilename(journal.Config) == filepath.Base(config) {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil, nil
	}
	backupDirectory, err := os.MkdirTemp(directory(config), filepath.Base(config)+".state.backup-")
	if err != nil {
		return nil, storageError()
	}
	backup := &localStateBackup{directory: backupDirectory}
	for _, path := range paths {
		if err := os.Rename(path, filepath.Join(backup.directory, filepath.Base(path))); err != nil {
			if restoreErr := backup.restore(); restoreErr != nil {
				return nil, restoreErr
			}
			return nil, storageError()
		}
		backup.originals = append(backup.originals, path)
	}
	return backup, nil
}

// A rejected or failed backend initialization leaves local tracking as it was.
func (backup *localStateBackup) restore() error {
	for index := len(backup.originals) - 1; index >= 0; index-- {
		original := backup.originals[index]
		if _, err := os.Lstat(original); !os.IsNotExist(err) {
			return backup.restoreError()
		}
		if err := os.Rename(filepath.Join(backup.directory, filepath.Base(original)), original); err != nil {
			return backup.restoreError()
		}
	}
	_ = os.Remove(backup.directory)
	return nil
}

func (backup *localStateBackup) restoreError() error {
	return problem.New("local_state_restore_failed", fmt.Sprintf("State initialization failed and local tracking could not be fully restored. Its backup is in %s. Restore the remaining files before retrying.", backup.directory), 5)
}
