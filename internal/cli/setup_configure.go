package cli

import (
	"fmt"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func collectSetup(input *setupInput, flags setupOptions) (manifest.SaaSOptions, error) {
	options := manifest.SaaSOptions{Name: flags.name, CreditsPerUSD: flags.credits}
	ui := input.ui()
	ui.heading("App")
	defaults := manifest.SaaSOptions{Name: "My app", Models: []string{"*"}, CreditsPerUSD: flags.credits}
	if flags.defaults != nil {
		defaults = *flags.defaults
	}
	var err error
	if options.Name == "" {
		options.Name, err = input.ask("App name", defaults.Name)
		if err != nil {
			return options, err
		}
	}
	options.Models = flags.models
	if len(options.Models) == 0 {
		hint := strings.Join(defaults.Models, ", ")
		if hint == "*" {
			hint = "* = all"
		}
		models, err := input.question("Models", strings.Join(defaults.Models, ", "), hint)
		if err != nil {
			return options, err
		}
		for _, model := range strings.Split(models, ",") {
			options.Models = append(options.Models, strings.TrimSpace(model))
		}
	}
	options.CreditsPerUSD, err = input.ask("Credits per USD", defaults.CreditsPerUSD)
	if err != nil {
		return options, err
	}
	if len(flags.plans) > 0 {
		return saasOptions(options.Name, options.Models, options.CreditsPerUSD, flags.plans)
	}
	// Validate app-level input before starting the plan loop.
	if _, err := saasOptions(options.Name, options.Models, options.CreditsPerUSD, nil); err != nil {
		return options, err
	}
	ui.heading("Plans")
	ui.note("Monthly AI allowance per customer.")
	for _, existing := range defaults.Plans {
		plan, err := editSetupPlan(input, options, existing)
		if err != nil {
			return options, err
		}
		options.Plans = append(options.Plans, plan)
		showSetupPlan(ui, plan, options.CreditsPerUSD)
	}
	if len(options.Plans) == 0 {
		plan, err := collectSetupPlan(input, options)
		if err != nil {
			return options, err
		}
		options.Plans = append(options.Plans, plan)
		showSetupPlan(ui, plan, options.CreditsPerUSD)
	}
	for {
		addAnother, err := input.confirm("Add another plan?", false)
		if err != nil {
			return options, err
		}
		if !addAnother {
			return options, nil
		}
		plan, err := collectSetupPlan(input, options)
		if err != nil {
			return options, err
		}
		options.Plans = append(options.Plans, plan)
		showSetupPlan(ui, plan, options.CreditsPerUSD)
	}
}

func showSetupPlan(ui terminalUI, plan manifest.SaaSPlan, rate string) {
	credits := readableCredits(manifest.CreditAllowance(plan.Allowance, rate))
	ui.success(fmt.Sprintf("%s: $%s / month, %s credits", plan.Name, plan.Allowance, credits))
}

func collectSetupPlan(input *setupInput, options manifest.SaaSOptions) (manifest.SaaSPlan, error) {
	defaultName := "Plus"
	if len(options.Plans) > 0 {
		defaultName = ""
	}
	return editSetupPlan(input, options, manifest.SaaSPlan{Name: defaultName, Allowance: "10"})
}

func editSetupPlan(input *setupInput, options manifest.SaaSOptions, initial manifest.SaaSPlan) (manifest.SaaSPlan, error) {
	for {
		name, err := input.ask("Plan name", initial.Name)
		if err != nil {
			return manifest.SaaSPlan{}, err
		}
		if name == "" {
			input.ui().warning("Enter a plan name.")
			continue
		}
		reference := initial.Reference
		if reference == "" {
			reference, err = setupPlanKey(input, options, name)
			if err != nil {
				return manifest.SaaSPlan{}, err
			}
		}
		for {
			allowance, err := input.ask("Allowance, USD", initial.Allowance)
			if err != nil {
				return manifest.SaaSPlan{}, err
			}
			plan := manifest.SaaSPlan{Reference: reference, Name: name, Allowance: allowance}
			if err := checkSetupPlan(options, plan); err != nil {
				input.ui().warning(setupPlanError(err))
				continue
			}
			return plan, nil
		}
	}
}

func setupPlanKey(input *setupInput, options manifest.SaaSOptions, name string) (string, error) {
	key := manifest.PlanReference(name)
	for {
		plan := manifest.SaaSPlan{Reference: key, Name: name, Allowance: "10"}
		err := checkSetupPlan(options, plan)
		if err == nil {
			return key, nil
		}
		failure := problem.Safe(err)
		invalidKey := failure.Code == "invalid_format" && failure.Pointer != nil && strings.HasPrefix(*failure.Pointer, "/keys/") && strings.Count(*failure.Pointer, "/") == 2
		if failure.Code != "duplicate_plan_reference" && !invalidKey {
			return "", err
		}
		input.ui().warning(setupPlanError(err))
		key, err = input.ask("JSON key", "")
		if err != nil {
			return "", err
		}
	}
}

func checkSetupPlan(options manifest.SaaSOptions, plan manifest.SaaSPlan) error {
	candidate := options
	candidate.Plans = append(append([]manifest.SaaSPlan(nil), options.Plans...), plan)
	_, err := manifest.SaaS(candidate)
	return err
}

func setupPlanError(err error) string {
	failure := problem.Safe(err)
	switch failure.Code {
	case "duplicate_plan_reference":
		return "JSON key already used. Choose another."
	case "invalid_decimal":
		return "Enter a positive decimal allowance, such as 10 or 2.50."
	case "decimal_out_of_bounds":
		return "Use at most 12 whole digits and 12 decimal places."
	case "invalid_format":
		if failure.Pointer != nil && strings.HasSuffix(*failure.Pointer, "/cap/amount") {
			return "Enter a positive decimal allowance, such as 10 or 2.50."
		}
		if failure.Pointer != nil && strings.HasPrefix(*failure.Pointer, "/keys/") && strings.Count(*failure.Pointer, "/") == 2 {
			return "Use 1 to 64 lowercase letters, digits, - or _ for the JSON key."
		}
	}
	return failure.Message
}

func readableCredits(value string) string {
	whole, fraction, hasFraction := strings.Cut(value, ".")
	for position := len(whole) - 3; position > 0; position -= 3 {
		whole = whole[:position] + "," + whole[position:]
	}
	if hasFraction {
		return whole + "." + fraction
	}
	return whole
}
