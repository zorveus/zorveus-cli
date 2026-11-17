package provision

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

type organizationStore struct{ saved auth.SavedLogins }

func (store *organizationStore) Read() (auth.SavedLogins, error) { return store.saved, nil }
func (store *organizationStore) Write(saved auth.SavedLogins) error {
	store.saved = saved
	return nil
}

func withOtherDefault(service *Service, login auth.Login) {
	other := login
	other.Organization = "org_b"
	other.AccessToken = "other-org-token"
	service.Auth.Store = &organizationStore{auth.SavedLogins{Version: 1, Logins: []auth.Login{login, other},
		Default: auth.LoginReference{APIOrigin: other.APIOrigin, Organization: other.Organization}}}
}

func TestApplyUsesPlanOrganizationDespiteDifferentDefaultAndRecordsItInManifest(t *testing.T) {
	service, backend, store, config, path := setup(t)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	withOtherDefault(service, store.login)
	if _, err := service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatal(err)
	}
	desired, _, _, err := loadManifest(config)
	if err != nil || desired.OrganizationID != "org_a" || desired.App.text("id") != "app_a" || len(backend.writes) == 0 {
		t.Fatalf("apply lost its organization or IDs: %v", err)
	}
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatalf("next plan used the other default organization: %v", err)
	}
	if status, err := service.Status(context.Background(), "", config); err != nil || status.Target.Org != "org_a" {
		t.Fatalf("status did not use the manifest organization: %v", err)
	}
}

func TestConflictingOrganizationFlagCannotApplySavedPlan(t *testing.T) {
	service, backend, _, config, path := setup(t)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	service.Auth.Organization = "org_b"
	_, err := service.Apply(context.Background(), "", path, true, false)
	if problem.Safe(err).Code != "organization_mismatch" || len(backend.writes) != 0 {
		t.Fatalf("apply accepted a conflicting organization: %v", err)
	}
}

func TestOlderManifestUsesOrganizationFromStateInsteadOfDefault(t *testing.T) {
	service, _, store, config, path := setup(t)
	planAndApply(t, service, config, path)
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var document Fields
	_ = json.Unmarshal(data, &document)
	delete(document, "organizationId")
	if err := os.WriteFile(config, encoded(document), 0600); err != nil {
		t.Fatal(err)
	}
	withOtherDefault(service, store.login)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatalf("legacy manifest lost its organization: %v", err)
	}
	var plan Plan
	_, _, _ = loadJSON(path, &plan)
	if plan.Target.Org != "org_a" {
		t.Fatal("legacy manifest used the default instead of its state")
	}
}

func TestImportWritesOrganizationAlongsideResourceIDs(t *testing.T) {
	service, _, _, config, path := setup(t)
	planAndApply(t, service, config, path)
	imported := filepath.Join(filepath.Dir(config), "imported.json")
	if _, err := service.Import(context.Background(), "", "app_a", imported); err != nil {
		t.Fatal(err)
	}
	desired, _, _, err := loadManifest(imported)
	if err != nil || desired.OrganizationID != "org_a" || desired.App.text("id") != "app_a" {
		t.Fatalf("import lost organization or app ID: %v", err)
	}
}

func TestApplyStopsAfterDifferentOrganizationLoginAndCanResumeOriginalPlan(t *testing.T) {
	service, backend, store, config, path := setup(t)
	if _, err := service.Plan(context.Background(), "", config, path); err != nil {
		t.Fatal(err)
	}
	accounts := &organizationStore{auth.SavedLogins{Version: 1, Logins: []auth.Login{store.login}}}
	service.Auth.Store = accounts
	now := time.Now()
	service.Auth.Now = func() time.Time {
		if len(backend.writes) > 0 {
			return now.Add(2 * time.Hour)
		}
		return now
	}
	service.Auth.RequestLogin = func(context.Context, auth.LoginOptions) (auth.LoginSummary, error) {
		other := store.login
		other.Organization = "org_b"
		other.AccessToken = "other-org-access"
		other.ExpiresAt = service.Auth.Now().Add(time.Hour)
		accounts.saved.Logins = append(accounts.saved.Logins, other)
		return auth.LoginSummary{APIOrigin: other.APIOrigin, Organization: other.Organization}, nil
	}
	_, err := service.Apply(context.Background(), "", path, true, false)
	if problem.Safe(err).Code != "target_mismatch" || len(backend.writes) != 1 || backend.writes[0] != "POST /apps" || len(accounts.saved.Logins) != 2 {
		t.Fatalf("switching organizations lost the new login or sent the next write: %v writes=%v", err, backend.writes)
	}
	accounts.saved.Logins[0].ExpiresAt = service.Auth.Now().Add(time.Hour)
	if _, err := service.Apply(context.Background(), "", path, true, false); err != nil {
		t.Fatalf("could not resume with the original organization's login: %v", err)
	}
	creations := 0
	for _, write := range backend.writes {
		if write == "POST /apps" {
			creations++
		}
	}
	if creations != 1 {
		t.Fatal("resuming repeated app creation")
	}
}
