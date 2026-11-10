package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func (flow *setupFlow) existingManifest() (bool, error) {
	document, err := manifest.Load(flow.options.config)
	if err != nil {
		return false, err
	}
	defaults, err := showExistingSetup(flow.input.ui(), flow.options.config, document)
	if err != nil {
		return false, err
	}
	useExisting, err := flow.input.confirm("Use this configuration?", true)
	if err != nil || useExisting {
		return useExisting, err
	}
	rewrite, err := flow.input.confirm("Rewrite this configuration?", false)
	if err != nil {
		return false, err
	}
	if !rewrite {
		flow.input.ui().note("Canceled. Existing configuration kept.")
		return false, nil
	}
	target := provision.Target{Origin: flow.identity.APIOrigin, Org: flow.identity.Organization, User: flow.identity.User.ID}
	if err := provision.CheckSetupState(flow.options.config, target, document.AppID()); err != nil {
		return false, err
	}
	expected, err := document.Bytes()
	if err != nil {
		return false, err
	}
	editable := flow.options
	editable.defaults = &defaults
	flow.input.ui().note("Existing app and key IDs will be kept.")
	configuration, err := collectSetup(flow.input, editable)
	if err != nil {
		return false, err
	}
	replacement, err := manifest.RewriteSaaS(document, configuration)
	if err != nil {
		return false, err
	}
	save, err := flow.input.confirm("Save and review plan?", true)
	if err != nil || !save {
		return false, err
	}
	if _, err := provision.RewriteSetupManifest(flow.input.ctx, flow.options.config, expected, replacement); err != nil {
		return false, err
	}
	flow.input.ui().success("Configuration rewritten. Previous file backed up.")
	return true, nil
}

func showExistingSetup(ui terminalUI, path string, document *manifest.Document) (manifest.SaaSOptions, error) {
	data, err := document.Bytes()
	if err != nil {
		return manifest.SaaSOptions{}, err
	}
	var saved struct {
		App struct {
			Name    string   `json:"name"`
			Models  []string `json:"allowedModels"`
			Credits string   `json:"creditUnitsPerUsd"`
		} `json:"app"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return manifest.SaaSOptions{}, err
	}
	ui.heading("Existing configuration")
	ui.field("File", path)
	ui.field("App", setupDetail(saved.App.Name))
	if document.AppID() != "" {
		ui.field("App ID", document.AppID())
	} else {
		ui.note("Not applied yet.")
	}
	models := strings.Join(saved.App.Models, ", ")
	if len(saved.App.Models) == 1 && saved.App.Models[0] == "*" {
		models = "All models (*)"
	}
	ui.field("Models", setupDetail(models))
	ui.field("Credits/USD", setupDetail(saved.App.Credits))
	defaults := manifest.SaaSOptions{Name: saved.App.Name, Models: saved.App.Models, CreditsPerUSD: saved.App.Credits}
	if defaults.Name == "" {
		defaults.Name = "My app"
	}
	if len(defaults.Models) == 0 {
		defaults.Models = []string{"*"}
	}
	if defaults.CreditsPerUSD == "" {
		defaults.CreditsPerUSD = "100"
	}
	for _, key := range document.Resources("keys") {
		name, _ := key.Fields["name"].(string)
		if name == "" {
			name = key.Name
		}
		cap, _ := key.Fields["cap"].(map[string]any)
		amount, _ := cap["amount"].(string)
		currency, _ := cap["currency"].(string)
		period, _ := cap["period"].(string)
		detail := "Allowance not specified"
		if amount != "" {
			detail = fmt.Sprintf("%s %s / %s per customer", amount, currency, period)
		}
		ui.field("Plan: "+name, detail)
		if amount == "" {
			amount = "10"
		}
		defaults.Plans = append(defaults.Plans, manifest.SaaSPlan{Reference: key.Name, Name: name, Allowance: amount})
	}
	for _, section := range []struct{ key, label string }{{"serviceKeys", "Service keys"}, {"routingKeys", "Routing keys"}, {"providerCredentials", "Providers"}} {
		if count := len(document.Resources(section.key)); count > 0 {
			ui.field(section.label, fmt.Sprint(count))
		}
	}
	return defaults, nil
}

func setupDetail(value string) string {
	if value == "" {
		return "Not specified"
	}
	return value
}
