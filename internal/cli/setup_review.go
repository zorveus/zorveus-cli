package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/provision"
)

func setupApplyQuestion(plan provision.PlanResult) string {
	for _, step := range plan.Changes {
		action, _ := planAction(step)
		if action != "+" {
			return "Apply these changes to your Zorveus account?"
		}
	}
	return "Create these resources in your Zorveus account?"
}

// The wizard shows every changed field without repeating Terraform-style braces
// or instructions for commands it is about to run itself.
func formatSetupPlan(result provision.PlanResult, color bool) string {
	var output strings.Builder
	ui := terminalUI{output: &output, color: color}
	ui.heading("Review")
	if len(result.Changes) == 0 {
		ui.success("No changes. Your configuration matches Zorveus.")
		ui.field("Saved plan", result.Plan)
		return output.String()
	}
	ui.note("+ create   ~ update   - remove")
	added, changed, removed := 0, 0, 0
	allowDelete := false
	for _, step := range result.Changes {
		allowDelete = allowDelete || step.Destructive
		symbol, _ := planAction(step)
		switch symbol {
		case "+":
			added++
		case "~":
			changed++
		case "-":
			removed++
		}
		fmt.Fprintf(&output, "\n  %s %s\n", planSymbol(symbol, color), ui.text(planAddress(step), terminalBold))
		if step.ID != "" {
			fmt.Fprintf(&output, "    %s\n", ui.text(step.ID, terminalMuted))
		}
		if step.Kind == "provider_secret" {
			writeSetupField(&output, ui, "Secret version", setupValue(step.Before["active_secret_version_id"])+" → rotated on apply")
		} else {
			for _, change := range step.FieldChanges() {
				value := setupFieldValue(change.Field, change.Desired)
				if symbol != "+" {
					value = setupFieldValue(change.Field, change.Actual) + " → " + value
				}
				writeSetupField(&output, ui, setupFieldLabel(change.Field), value)
			}
		}
		if step.SecretEnv != "" {
			writeSetupField(&output, ui, "Secret from env", terminalText(step.SecretEnv))
		}
	}
	fmt.Fprintf(&output, "\n  %s to add, %s to change, %s to remove\n", ui.text(fmt.Sprint(added), terminalSuccess), ui.text(fmt.Sprint(changed), terminalWarning), ui.text(fmt.Sprint(removed), terminalError))
	if allowDelete {
		ui.warning("Removals and revocations require --allow-delete.")
	}
	ui.field("Saved plan", result.Plan)
	return output.String()
}

func writeSetupField(output *strings.Builder, ui terminalUI, label, value string) {
	value = strings.ReplaceAll(value, "\n", "\n                       ")
	fmt.Fprintf(output, "    %s %s\n", ui.text(fmt.Sprintf("%-18s", label), terminalMuted), value)
}

func setupFieldLabel(name string) string {
	switch name {
	case "app_name", "key_alias", "credential_name":
		return "Name"
	case "allowed_models", "default_model_policy":
		return "Models"
	case "client_type":
		return "OAuth client"
	case "credit_units_per_usd":
		return "Credits per USD"
	case "approved_scopes", "scopes":
		return "Scopes"
	case "credit_mode":
		return "Credit policy"
	case "overrun_policy":
		return "Overrun policy"
	}
	label := strings.ReplaceAll(name, "_", " ")
	if label == "" {
		return label
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

func setupFieldValue(name string, raw json.RawMessage) string {
	if name == "allowed_models" || name == "default_model_policy" {
		var models []string
		if json.Unmarshal(raw, &models) == nil && len(models) == 1 && models[0] == "*" {
			return "All models (*)"
		}
	}
	return setupValue(raw)
}

func setupValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "(not set)"
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return terminalText(text)
	}
	var values []string
	if json.Unmarshal(raw, &values) == nil && len(values) > 0 {
		return terminalText(strings.Join(values, ", "))
	}
	return formatPlanValue(raw)
}
