package selfupdate

import (
	"errors"
	"os"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func replaceExecutable(executable, staged, system string) error {
	if system != "windows" {
		if err := os.Rename(staged, executable); err != nil {
			return replacementError()
		}
		return nil
	}
	// Windows needs the running image moved aside before installing its replacement.
	backup := executable + ".zorveus-update-old"
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return replacementError()
	}
	if err := os.Rename(executable, backup); err != nil {
		return replacementError()
	}
	if err := os.Rename(staged, executable); err != nil {
		if restoreErr := os.Rename(backup, executable); restoreErr != nil {
			return problem.New("update_restore_failed", "Could not install the update or restore the previous executable. Restore it from "+backup+".", 1)
		}
		return replacementError()
	}
	// An in-use image may remain until a later update, after this process exits.
	_ = os.Remove(backup)
	return nil
}
