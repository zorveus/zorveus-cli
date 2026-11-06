package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestAcceptedFixturesAndDocumentedExamples(t *testing.T) {
	paths, err := filepath.Glob("testdata/valid/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatal("accepted fixtures are missing")
	}
	examplePaths, err := filepath.Glob("../../examples/*.json")
	if err != nil || len(examplePaths) == 0 {
		t.Fatal("example manifests are missing")
	}
	paths = append(paths, examplePaths...)
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			if _, err := Load(path); err != nil {
				t.Fatal(err)
			}
		})
	}
	proposal, err := os.ReadFile("../../zorveus_cli_and_manifest_proposal.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range regexp.MustCompile("(?s)~~~json\\n(.*?)\\n~~~").FindAllSubmatch(proposal, -1) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(match[1], &fields); err != nil {
			t.Fatal(err)
		}
		if fields["version"] == nil {
			continue
		}
		if _, err := Parse(match[1]); err != nil {
			t.Fatalf("a documented manifest was rejected: %v", err)
		}
	}
}

func TestRejectedFixtures(t *testing.T) {
	data, err := os.ReadFile("testdata/invalid.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string `json:"name"`
		Document string `json:"document"`
		Code     string `json:"code"`
		Pointer  string `json:"pointer"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			_, err := Parse([]byte(test.Document))
			assertProblem(t, err, test.Code, test.Pointer)
			if strings.Contains(err.Error(), "private-secret") {
				t.Fatal("validation exposed a value from the manifest")
			}
		})
	}
}

func TestPartialUpdatesReportRemoteChecks(t *testing.T) {
	document, err := Parse([]byte(`{
		"version":1,
		"app":{"id":"app_existing"},
		"routingKeys":[
			{"id":"router_existing","members":["remote-alias"],"defaultSelector":"remote-alias"},
			{"id":"router_other","defaultSelector":"existing-member"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	checks := document.Result("zorveus.json").DeferredChecks
	if len(checks) != 2 || checks[0].Code != "remote_alias_required" || checks[1].Code != "remote_members_required" {
		t.Fatalf("missing deferred reference checks: %+v", checks)
	}
	document, err = Parse([]byte(`{"version":1,"app":{"id":"app_existing"},"keys":[{"id":"key_existing","routingAlias":"backend"}]}`))
	if err != nil || len(document.Result("").DeferredChecks) != 1 {
		t.Fatalf("omitted app models should defer inheritance: %v", err)
	}
}

func TestNullableFieldsAndRouterClearingPreserveMeaning(t *testing.T) {
	source := []byte(`{"version":1,"app":{"id":"app_existing","creditUnitsPerUsd":null,"consent":{"defaultCap":null,"minimumCapAmount":null}},"keys":[{"id":"key_existing","routingAlias":"backend","cap":null,"productUserChargeMultiplier":null}],"routingKeys":[{"id":"router_existing","members":[],"defaultSelector":null}]}`)
	document, err := Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(document.data)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"cap":null`, `"members":[]`, `"defaultSelector":null`, `"creditUnitsPerUsd":null`} {
		if !bytes.Contains(encoded, []byte(field)) {
			t.Fatalf("explicit clearing was lost: %s", field)
		}
	}
	if bytes.Contains(encoded, []byte("serviceKeys")) {
		t.Fatal("an omitted resource section was inserted")
	}
}

func TestDecimalsStayExactAndAcceptBackendBoundary(t *testing.T) {
	source := []byte(`{"version":1,"app":{"name":"Exact amounts","allowedModels":["*"],"creditUnitsPerUsd":"999999999999.999999999999","productUserChargeMultiplier":"999999999999.999999"},"keys":[{"name":"Backend","routingAlias":"backend","allowedModels":["model_a"],"cap":{"amount":"0.000000000001","currency":"USD","period":"lifetime"}}]}`)
	document, err := Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeAndValidate(document.data)
	if err != nil || !bytes.Contains(encoded, []byte(`"999999999999.999999999999"`)) || !bytes.Contains(encoded, []byte(`"0.000000000001"`)) {
		t.Fatal("decimal text changed")
	}
}

func TestValidationDoesNotReadProviderSecretInputs(t *testing.T) {
	t.Setenv("ZORVEUS_TEST_PROVIDER_SECRET", "private-secret")
	source := []byte(`{"version":1,"app":{"name":"Provider example","allowedModels":["model_a"]},"providerCredentials":[{"name":"Provider","provider":"openai","secretFromEnv":"ZORVEUS_TEST_PROVIDER_SECRET"}]}`)
	document, err := Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(document.data)
	if bytes.Contains(encoded, []byte("private-secret")) {
		t.Fatal("validation expanded an environment variable")
	}
	t.Setenv("ZORVEUS_TEST_PROVIDER_SECRET", "")
	if _, err := Parse(source); err != nil {
		t.Fatal("offline validation required a secret input")
	}
}

func TestParserRejectsOversizedAndDeepInput(t *testing.T) {
	_, err := Parse(bytes.Repeat([]byte(" "), maxFileSize+1))
	assertProblem(t, err, "manifest_too_large", "")
	_, err = Parse([]byte(strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)))
	if err == nil || problem.Safe(err).Code != "json_too_deep" {
		t.Fatal("excessive JSON nesting was accepted")
	}
}

func assertProblem(t *testing.T, err error, code, pointer string) {
	t.Helper()
	var failure *problem.Error
	if !errors.As(err, &failure) || failure.Code != code || failure.Pointer == nil || *failure.Pointer != pointer || failure.ExitCode != 2 {
		t.Fatalf("expected %s at %q, got %#v", code, pointer, err)
	}
}
