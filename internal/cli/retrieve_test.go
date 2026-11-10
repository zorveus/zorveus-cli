package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetrieveRequiresOneItemBeforeReadingCredentials(t *testing.T) {
	for _, arguments := range [][]string{{"retrieve"}, {"retrieve", "app", "keys.plus"}, {"exec", "--", "npm"}} {
		var stdout, stderr bytes.Buffer
		if code := Execute(context.Background(), arguments, &stdout, &stderr, Dependencies{}); code != 2 {
			t.Fatalf("invalid command accepted: %d %s %s", code, &stdout, &stderr)
		}
	}
}

func TestRetrieveUnknownItemDoesNotReadCredentialsOrPrintSecrets(t *testing.T) {
	config := filepath.Join(t.TempDir(), "zorveus.json")
	if err := os.WriteFile(config, []byte(`{"version":1,"app":{"id":"app_a","name":"Demo"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	dependencies := testDependencies(&testStore{err: errors.New("private credential-store error")})
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"retrieve", "keys.unknown", "--config", config, "--json"}, &stdout, &stderr, dependencies)
	if code != 2 || !strings.Contains(stdout.String(), "item_not_found") || strings.Contains(stdout.String()+stderr.String(), "private") {
		t.Fatalf("invalid selection reached credentials: code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
}

func TestRetrieveProviderDoesNotPrintItsEnvironmentSecret(t *testing.T) {
	config := filepath.Join(t.TempDir(), "zorveus.json")
	data := `{"version":1,"app":{"name":"Demo","allowedModels":["*"]},"providerCredentials":{"openai":{"name":"OpenAI","provider":"openai","secretFromEnv":"RETRIEVE_PROVIDER_KEY"}}}`
	if err := os.WriteFile(config, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RETRIEVE_PROVIDER_KEY", "private-provider-secret")
	dependencies := testDependencies(&testStore{err: errors.New("private credential-store error")})
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"retrieve", "providerCredentials.openai", "--config", config, "--json"}, &stdout, &stderr, dependencies)
	if code != 2 || !strings.Contains(stdout.String(), "secret_not_available") || strings.Contains(stdout.String()+stderr.String(), "private") {
		t.Fatalf("provider retrieval did not explain its source: code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
}
