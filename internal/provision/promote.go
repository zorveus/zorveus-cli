package provision

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

type PromotionResult struct {
	Source  string `json:"source"`
	Config  string `json:"config"`
	Changed bool   `json:"changed"`
}

// Promote changes only the target manifest. Plan and apply still own deployment.
func Promote(ctx context.Context, source, target string) (PromotionResult, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return PromotionResult{}, storageError()
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return PromotionResult{}, storageError()
	}
	sourceInfo, sourceErr := os.Lstat(source)
	targetInfo, targetErr := os.Lstat(target)
	if sourceErr != nil || targetErr != nil || !sourceInfo.Mode().IsRegular() || !targetInfo.Mode().IsRegular() {
		return PromotionResult{}, problem.New("promotion_files_required", "Provide existing regular manifest files with --from and --to. Import the target app first if it has no manifest.", 2)
	}
	if os.SameFile(sourceInfo, targetInfo) {
		return PromotionResult{}, problem.New("promotion_same_file", "Promotion requires different source and target manifest files.", 2)
	}
	unlock, err := lockConfig(ctx, target)
	if err != nil {
		return PromotionResult{}, err
	}
	defer unlock()
	if err := requireFinishedApply(target); err != nil {
		return PromotionResult{}, err
	}
	sourceDocument, err := manifest.Load(source)
	if err != nil {
		return PromotionResult{}, err
	}
	_, original, canonical, err := loadManifest(target)
	if err != nil {
		return PromotionResult{}, err
	}
	targetDocument, err := manifest.Parse(canonical)
	if err != nil {
		return PromotionResult{}, err
	}
	promoted, err := manifest.Promote(sourceDocument, targetDocument)
	if err != nil {
		return PromotionResult{}, err
	}
	result := PromotionResult{Source: source, Config: target, Changed: !bytes.Equal(canonical, promoted)}
	if !result.Changed {
		return result, nil
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return PromotionResult{}, storageError()
	}
	if !bytes.Equal(current, original) {
		return PromotionResult{}, problem.New("config_changed", "The target manifest changed during promotion. Its edits were preserved; review them and retry.", 4)
	}
	if err := ctx.Err(); err != nil {
		return PromotionResult{}, err
	}
	if err := replaceFile(target, promoted); err != nil {
		return PromotionResult{}, err
	}
	return result, nil
}
