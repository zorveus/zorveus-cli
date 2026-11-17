package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/auth"
)

func TestSignInPromptShowsCachedNameOrJustOrganizationID(t *testing.T) {
	for _, test := range []struct{ name, origin, want string }{
		{"My Startup", "http://localhost:8000", "My Startup (org_a)"},
		{"", "http://localhost:8000", "org_a"},
		{"  ", "http://localhost:8000", "org_a"},
		{"Production name", "https://api.zorveus.com", "org_a"},
	} {
		t.Run(test.name+test.origin, func(t *testing.T) {
			// Only metadata remains; no token is available to look up the name remotely.
			dependencies := organizationDependencies(t, auth.Login{
				APIOrigin: test.origin, Organization: "org_a", OrganizationName: test.name,
			})
			dependencies.InputIsTerminal = true
			dependencies.Stdin = strings.NewReader("")
			dependencies.OpenBrowser = func(context.Context, string) error {
				t.Fatal("browser opened without Enter")
				return nil
			}
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), []string{"whoami", "--local", "--org", "org_a"}, &stdout, &stderr, dependencies)
			want := "Sign in to organization " + test.want + " to continue."
			if code != 3 || !strings.Contains(stderr.String(), want) {
				t.Fatalf("incorrect sign-in label: code=%d want=%q stderr=%s", code, want, &stderr)
			}
		})
	}
}
