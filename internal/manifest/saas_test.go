package manifest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSaaSAllowsAllModelsByDefault(t *testing.T) {
	for _, models := range [][]string{nil, {"*"}} {
		options := SaaSOptions{Name: "Demo", Models: models, CreditsPerUSD: "100", Plans: []SaaSPlan{{Reference: "plus", Name: "Plus", Allowance: "10"}}}
		data, err := SaaS(options)
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			App struct {
				AllowedModels []string `json:"allowedModels"`
			} `json:"app"`
		}
		if err := json.Unmarshal(data, &document); err != nil || len(document.App.AllowedModels) != 1 || document.App.AllowedModels[0] != "*" {
			t.Fatal("SaaS manifest did not allow all models")
		}
	}
}

func TestSaaSRoutingUsesPlanReferencesAndAllowsChoosingTheDefault(t *testing.T) {
	options := SaaSOptions{Name: "Demo", CreditsPerUSD: "100", Plans: []SaaSPlan{
		{Reference: "plus", Name: "Plus", Allowance: "10"},
		{Reference: "pro", Name: "Pro", Allowance: "30"},
	}, Routing: &SaaSRouting{DefaultSelector: "pro"}}
	data, err := SaaS(options)
	if err != nil {
		t.Fatal(err)
	}
	document, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	router := document.Resources("routingKeys")[0]
	if router.Name != "primary" || router.Fields["name"] != "Demo router" || router.Fields["defaultSelector"] != "pro" || !reflect.DeepEqual(router.Fields["members"], []any{"plus", "pro"}) {
		t.Fatal("routing did not use the collected plan references")
	}
	options.Routing.DefaultSelector = ""
	data, err = SaaS(options)
	if err != nil {
		t.Fatal(err)
	}
	document, _ = Parse(data)
	if document.Resources("routingKeys")[0].Fields["defaultSelector"] != "plus" {
		t.Fatal("routing did not default to the first collected plan")
	}
	options.Routing.DefaultSelector = "missing"
	if _, err := SaaS(options); err == nil {
		t.Fatal("routing accepted a default outside its members")
	}
}

func TestSaaSBuildsNamedPlansWithExactAllowances(t *testing.T) {
	options := SaaSOptions{Name: "Writing app", Models: []string{"model_a"}, CreditsPerUSD: "100", Plans: []SaaSPlan{
		{Reference: "plus", Name: "Plus", Allowance: "10"},
		{Reference: "premium", Name: "Premium", Allowance: "30.000000000001"},
	}}
	data, err := SaaS(options)
	if err != nil {
		t.Fatal(err)
	}
	document, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	keys := document.Resources("keys")
	if len(keys) != 2 || keys[0].Name != "plus" || keys[1].Name != "premium" || len(document.Resources("routingKeys")) != 0 {
		t.Fatal("unexpected plans or routing resources")
	}
	cap := keys[1].Fields["cap"].(map[string]any)
	if cap["amount"] != options.Plans[1].Allowance || keys[0].Fields["creditMode"] != "enforce" || keys[0].Fields["overrunPolicy"] != "limit_output" {
		t.Fatal("plan policy changed")
	}
	if CreditAllowance(options.Plans[1].Allowance, options.CreditsPerUSD) != "3000.0000000001" || CreditAllowance("0.000000000001", "0.000000000001") != "0.000000000000000000000001" {
		t.Fatal("credit conversion lost decimal precision")
	}
	if strings.Contains(string(data), `"id"`) || strings.Contains(string(data), `"secret"`) || !strings.Contains(string(data), `"product_users:read"`) || strings.Contains(string(data), `"product_users:write"`) {
		t.Fatal("starter contains identity, secret, or unnecessary write scope")
	}
}

func TestSaaSRejectsInvalidInputsAndPreservesExistingFiles(t *testing.T) {
	valid := SaaSOptions{Name: "Demo", Models: []string{"model_a"}, CreditsPerUSD: "100", Plans: []SaaSPlan{{Reference: "plus", Name: "Plus", Allowance: "10"}}}
	for _, change := range []func(*SaaSOptions){
		func(o *SaaSOptions) { o.Models = []string{" model_a "} },
		func(o *SaaSOptions) { o.Name = " " },
		func(o *SaaSOptions) { o.CreditsPerUSD = "NaN" },
		func(o *SaaSOptions) { o.Plans = nil },
		func(o *SaaSOptions) { o.Plans = append(o.Plans, o.Plans[0]) },
		func(o *SaaSOptions) { o.Plans[0].Allowance = "0" },
		func(o *SaaSOptions) { o.Plans[0].Allowance = "1/3" },
		func(o *SaaSOptions) { o.Plans[0].Reference = "Bad name" },
	} {
		options := valid
		options.Plans = append([]SaaSPlan(nil), valid.Plans...)
		change(&options)
		if _, err := SaaS(options); err == nil {
			t.Fatalf("invalid setup accepted: %+v", options)
		}
	}
	path := filepath.Join(t.TempDir(), "zorveus.json")
	if _, err := InitSaaS(path, valid); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := InitSaaS(path, valid); err == nil {
		t.Fatal("existing manifest overwritten")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("existing manifest changed")
	}
}
