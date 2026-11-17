package provision

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestRecreatedManifestReportsPreviousAppBeforeSignIn(t *testing.T) {
	service, _, _, config, planPath := setup(t)
	planAndApply(t, service, config, planPath)
	previousState, _ := os.ReadFile(statePath(config))
	previousPlan, _ := os.ReadFile(planPath)
	if err := os.WriteFile(config, []byte(newManifest), 0600); err != nil {
		t.Fatal(err)
	}
	service.Auth.RequestLogin = func(context.Context, auth.LoginOptions) (auth.LoginSummary, error) {
		t.Fatal("local app mismatch prompted sign-in")
		return auth.LoginSummary{}, nil
	}
	service.Auth.API.HTTP.Transport = recoveryTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("local app mismatch made an API request")
		return nil, nil
	})
	_, err := service.Plan(context.Background(), "", config, planPath)
	failure := problem.Safe(err)
	if failure.Code != "state_target_mismatch" || !strings.Contains(failure.Message, "has no app.id") || !strings.Contains(failure.Message, "app_a") || !strings.Contains(failure.Message, statePath(config)) || !strings.Contains(failure.Message, "zorveus setup") {
		t.Fatalf("recreated manifest did not explain its previous app: %v", err)
	}
	stateAfter, _ := os.ReadFile(statePath(config))
	planAfter, _ := os.ReadFile(planPath)
	manifestAfter, _ := os.ReadFile(config)
	if !bytes.Equal(previousState, stateAfter) || !bytes.Equal(previousPlan, planAfter) || !bytes.Equal([]byte(newManifest), manifestAfter) {
		t.Fatal("reporting the conflict modified project files")
	}
}

func TestStateMismatchNamesTheSpecificOrganizationAPIOrApp(t *testing.T) {
	for _, test := range []struct {
		name   string
		target Target
		appID  string
		want   []string
	}{
		{"organization", Target{Origin: "https://api.example.test", Org: "org_b"}, "app_a", []string{"organization", "org_a", "org_b"}},
		{"API", Target{Origin: "https://other.example.test", Org: "org_a"}, "app_a", []string{"API server", "https://api.example.test", "https://other.example.test"}},
		{"app", Target{Origin: "https://api.example.test", Org: "org_a"}, "app_other", []string{"app.id", "app_a", "app_other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, _, config, path := setup(t)
			planAndApply(t, service, config, path)
			_, _, err := readState(config, test.target, test.appID)
			failure := problem.Safe(err)
			if failure.Code != "state_target_mismatch" || strings.Contains(failure.Message, "login target") {
				t.Fatalf("unexpected state mismatch: %v", err)
			}
			for _, text := range test.want {
				if !strings.Contains(failure.Message, text) {
					t.Fatalf("state mismatch omitted %q: %v", text, err)
				}
			}
			if test.name != "app" {
				_, err := ProjectTarget(config, test.target.Origin, test.target.Org)
				for _, text := range test.want {
					if !strings.Contains(problem.Safe(err).Message, text) {
						t.Fatalf("project selection mismatch omitted %q: %v", text, err)
					}
				}
			}
		})
	}
}
