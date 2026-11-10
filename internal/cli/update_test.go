package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/selfupdate"
)

func TestUpdateDoesNotRequireLoginAndKeepsJSONOutputClean(t *testing.T) {
	for _, current := range []bool{false, true} {
		called := false
		dependencies := Dependencies{UpdateCLI: func(_ context.Context, version string) (selfupdate.Result, error) {
			called = true
			if version != "v1.2.3" {
				t.Fatal("update lost the requested version")
			}
			return selfupdate.Result{Version: version, Executable: "/custom tools/zorveus", AlreadyCurrent: current}, nil
		}}
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"update", "--version", "v1.2.3", "--json", "--non-interactive"}, &stdout, &stderr, dependencies)
		var envelope struct {
			OK     bool              `json:"ok"`
			Result selfupdate.Result `json:"result"`
		}
		if code != 0 || !called || json.Unmarshal(stdout.Bytes(), &envelope) != nil || !envelope.OK || envelope.Result.AlreadyCurrent != current || envelope.Result.Executable != "/custom tools/zorveus" {
			t.Fatalf("update result was incorrect: %d %s %s", code, &stdout, &stderr)
		}
	}
}

func TestUpdateFailureAndArguments(t *testing.T) {
	dependencies := Dependencies{UpdateCLI: func(context.Context, string) (selfupdate.Result, error) {
		return selfupdate.Result{}, problem.New("update_checksum_mismatch", "Checksum verification failed.", 1)
	}}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"update", "--json"}, &stdout, &stderr, dependencies)
	if code != 1 || !strings.Contains(stdout.String(), "update_checksum_mismatch") {
		t.Fatalf("update failure was hidden: %d %s", code, &stdout)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Execute(context.Background(), []string{"update", "unexpected"}, &stdout, &stderr, Dependencies{}); code != 2 {
		t.Fatal("invalid update arguments reached the updater")
	}
}
