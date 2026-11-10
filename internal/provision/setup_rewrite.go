package provision

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/zorveus/zorveus-cli/internal/manifest"
)

func RewriteSetupManifest(ctx context.Context, config string, expected, replacement []byte) (string, error) {
	config, err := filepath.Abs(config)
	if err != nil {
		return "", storageError()
	}
	unlock, err := lockConfig(ctx, config)
	if err != nil {
		return "", err
	}
	defer unlock()
	if err := requireFinishedApply(config); err != nil {
		return "", err
	}
	current, original, canonical, err := loadManifest(config)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(expected, canonical) {
		return "", setupStateChanged()
	}
	document, err := manifest.Parse(replacement)
	if err != nil {
		return "", err
	}
	if document.AppID() != current.App.text("id") {
		return "", setupStateChanged()
	}
	backup, err := os.MkdirTemp(directory(config), "setup-backup-")
	if err != nil {
		return "", storageError()
	}
	if err := writeNew(filepath.Join(backup, filepath.Base(config)), original); err != nil {
		return "", err
	}
	if err := replaceFile(config, replacement); err != nil {
		return "", err
	}
	return backup, nil
}
