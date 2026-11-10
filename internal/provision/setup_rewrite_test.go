package provision

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestRewriteSetupManifestPreservesChangesMadeDuringQuestions(t *testing.T) {
	_, _, _, config, _ := setup(t)
	_, _, expected, err := loadManifest(config)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := manifest.SaaS(manifest.SaaSOptions{Name: "Wizard changes", CreditsPerUSD: "100", Plans: []manifest.SaaSPlan{{Reference: "plus", Name: "Plus", Allowance: "10"}}})
	if err != nil {
		t.Fatal(err)
	}
	concurrent := bytes.Replace([]byte(newManifest), []byte("Demo"), []byte("Another editor's changes"), 1)
	if err := os.WriteFile(config, concurrent, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = RewriteSetupManifest(context.Background(), config, expected, replacement)
	if err == nil || problem.Safe(err).Code != "setup_state_changed" {
		t.Fatalf("rewrite overwrote a changed file: %v", err)
	}
	after, _ := os.ReadFile(config)
	backups, _ := filepath.Glob(filepath.Join(directory(config), "setup-backup-*"))
	if !bytes.Equal(concurrent, after) || len(backups) != 0 {
		t.Fatal("rejected rewrite changed or backed up someone else's edit")
	}
}
