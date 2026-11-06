package provision

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func newID() string             { return rand.Text() }
func encoded(value any) []byte {
	result, _ := json.MarshalIndent(value, "", "  ")
	return append(result, '\n')
}
func directory(config string) string { return filepath.Join(filepath.Dir(config), ".zorveus") }
func statePath(config string) string {
	return filepath.Join(directory(config), filepath.Base(config)+".state.json")
}
func journalPath(plan Plan) string {
	return filepath.Join(directory(plan.Config), plan.ID+".apply.json")
}

func requireFinishedApply(config string) error {
	entries, err := os.ReadDir(directory(config))
	if err != nil {
		return storageError()
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".apply.json") {
			continue
		}
		var journal Journal
		_, _, err := loadJSON(filepath.Join(directory(config), entry.Name()), &journal)
		if err != nil {
			return err
		}
		if journal.Config == config && !journal.Complete {
			return problem.New("apply_pending", "This manifest has an unfinished apply. Resume its saved plan and inspect the local journal before making another plan.", 5)
		}
	}
	return nil
}

func lockConfig(ctx context.Context, config string) (func(), error) {
	dir := directory(config)
	if info, err := os.Lstat(dir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, storageError()
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, storageError()
	}
	lock := flock.New(filepath.Join(dir, filepath.Base(config)+".lock"), flock.SetPermissions(0600))
	locked, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil || !locked {
		_ = lock.Close()
		return nil, storageError()
	}
	return func() { _ = lock.Close() }, nil
}

func loadJSON(path string, value any) (bool, []byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return false, nil, storageError()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, nil, storageError()
	}
	if err := json.Unmarshal(data, value); err != nil {
		return false, nil, storageError()
	}
	return true, data, nil
}

func writeNew(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return problem.New("destination_exists", "Could not create the plan file. Choose a new --out path in an existing directory.", 1)
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(path)
		return storageError()
	}
	return nil
}

func replaceFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return storageError()
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".zorveus-write-*")
	if err != nil {
		return storageError()
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return storageError()
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return storageError()
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return storageError()
	}
	return nil
}

func loadManifest(path string) (Manifest, []byte, []byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Manifest{}, nil, nil, storageError()
	}
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, nil, nil, problem.New("config_read_failed", "Could not open the manifest.", 1)
	}
	defer file.Close()
	original, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil {
		return Manifest{}, nil, nil, storageError()
	}
	document, err := manifest.Parse(original)
	if err != nil {
		return Manifest{}, nil, nil, err
	}
	canonical, err := document.Bytes()
	if err != nil {
		return Manifest{}, nil, nil, err
	}
	var result Manifest
	_ = json.Unmarshal(canonical, &result)
	return result, original, canonical, nil
}

func storageError() error {
	return problem.New("local_state_unavailable", "Could not read or safely write the plan, manifest, or local apply state. Check file permissions and disk space.", 1)
}
