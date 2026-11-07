package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/provision"
)

func TestPromotionCommandWorksOfflineAndReturnsOneJSONResult(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "dev.json"), filepath.Join(dir, "prod.json")
	if err := os.WriteFile(source, []byte(`{"version":1,"app":{"id":"app_dev","name":"Updated"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"version":1,"app":{"id":"app_prod","name":"Old"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []bool{true, false} {
		var stdout, stderr bytes.Buffer
		arguments := []string{"promote", "--from", source, "--to", target, "--json", "--non-interactive"}
		if code := Execute(context.Background(), arguments, &stdout, &stderr, Dependencies{}); code != 0 {
			t.Fatalf("offline promotion failed: %d %s %s", code, stdout.String(), stderr.String())
		}
		var result struct {
			OK     bool                      `json:"ok"`
			Result provision.PromotionResult `json:"result"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || !result.OK || result.Result.Changed != changed || result.Result.Config != target || stderr.Len() != 0 {
			t.Fatalf("expected one successful promotion result: %s %s", stdout.String(), stderr.String())
		}
	}
	data, err := os.ReadFile(target)
	if err != nil || !bytes.Contains(data, []byte("app_prod")) || !bytes.Contains(data, []byte("Updated")) || bytes.Contains(data, []byte("app_dev")) {
		t.Fatal("promotion did not preserve target identity and copy source settings")
	}
}

func TestPromotionRequiresBothPaths(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"promote", "--from", "dev.json", "--json"}, &stdout, &stderr, Dependencies{})
	if code != 2 || !strings.Contains(stdout.String(), "invalid_arguments") {
		t.Fatal("promotion accepted a missing target")
	}
}
