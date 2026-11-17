package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOrganizationIDIsOptionalButMustBeAnID(t *testing.T) {
	for _, value := range []string{`"org_example"`, `null`, `""`, `"not an ID"`} {
		_, err := Parse([]byte(`{"version":1,"organizationId":` + value + `,"app":{"name":"Demo","allowedModels":["*"]}}`))
		if (err == nil) != (value == `"org_example"`) {
			t.Fatalf("incorrect organization validation for %s: %v", value, err)
		}
	}
}

func TestCloneClearsOrganizationAndPromotionPreservesTargetOrganization(t *testing.T) {
	dev, err := Parse([]byte(`{"version":1,"organizationId":"org_dev","app":{"id":"app_dev","name":"Development","allowedModels":["*"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	prod, err := Parse([]byte(`{"version":1,"organizationId":"org_prod","app":{"id":"app_prod","name":"Production","allowedModels":["*"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := Promote(dev, prod, false)
	if err != nil {
		t.Fatal(err)
	}
	document, err := Parse(promoted)
	if err != nil || document.OrganizationID() != "org_prod" || document.AppID() != "app_prod" {
		t.Fatalf("promotion copied the source organization or app ID: %v", err)
	}
	directory := t.TempDir()
	source, target := filepath.Join(directory, "source.json"), filepath.Join(directory, "clone.json")
	data, _ := dev.Bytes()
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Clone(source, target, "New project"); err != nil {
		t.Fatal(err)
	}
	cloned, err := Load(target)
	if err != nil || cloned.OrganizationID() != "" || cloned.AppID() != "" {
		t.Fatalf("clone retained the source deployment identity: %v", err)
	}
}
