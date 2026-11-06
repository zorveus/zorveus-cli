package cli

import (
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/provision"
)

func formatPlan(result provision.PlanResult, color bool) string {
	var output strings.Builder
	if len(result.Changes) == 0 {
		output.WriteString("No changes. Your configuration matches Zorveus.\n")
		fmt.Fprintf(&output, "\nSaved plan: %s\n", terminalText(result.Plan))
		return output.String()
	}
	fmt.Fprintf(&output, "Proposed changes:\n  %s create   %s update   %s remove or revoke\n\n", planSymbol("+", color), planSymbol("~", color), planSymbol("-", color))
	added, changed, removed := 0, 0, 0
	allowDelete := false
	for _, step := range result.Changes {
		symbol, description := planAction(step)
		switch symbol {
		case "+":
			added++
		case "~":
			changed++
		case "-":
			removed++
		}
		allowDelete = allowDelete || step.Destructive
		address := planAddress(step)
		fmt.Fprintf(&output, "  # %s %s\n", address, description)
		fmt.Fprintf(&output, "  %s %s {\n", planSymbol(symbol, color), address)
		if symbol == "+" {
			output.WriteString("      id = (known after apply)\n")
		} else if step.ID != "" && step.Kind != "key_cap" {
			fmt.Fprintf(&output, "      id = %s\n", strconv.Quote(step.ID))
		}
		if step.Kind == "provider_secret" {
			fmt.Fprintf(&output, "    %s active_secret_version_id = %s -> (known after apply)\n", planSymbol("~", color), formatPlanValue(step.Before["active_secret_version_id"]))
		} else {
			writePlanFields(&output, step, symbol, color)
		}
		if step.SecretEnv != "" {
			fmt.Fprintf(&output, "      secret_from_env = %s\n", strconv.Quote(step.SecretEnv))
		}
		output.WriteString("    }\n\n")
	}
	fmt.Fprintf(&output, "Plan: %s to add, %s to change, %s to remove.\n", planColor(strconv.Itoa(added), "32", color), planColor(strconv.Itoa(changed), "33", color), planColor(strconv.Itoa(removed), "31", color))
	if allowDelete {
		output.WriteString("\nThis plan includes removals or revocations and requires --allow-delete.\n")
	}
	fmt.Fprintf(&output, "\nSaved plan: %s\n\nTo apply:\n  zorveus apply --plan %s --yes", terminalText(result.Plan), quotePlanPath(result.Plan))
	if allowDelete {
		output.WriteString(" --allow-delete")
	}
	output.WriteByte('\n')
	return output.String()
}

func planAction(step provision.Step) (string, string) {
	switch {
	case strings.HasPrefix(step.Kind, "create_"), step.Kind == "app_redirect_add":
		return "+", "will be created"
	case strings.HasPrefix(step.Kind, "revoke_"):
		return "-", "will be revoked"
	case step.Kind == "app_redirect_remove", step.Kind == "key_cap" && step.After == nil:
		return "-", "will be removed"
	case step.Kind == "key_cap" && step.Before == nil:
		return "+", "will be created"
	case step.Kind == "provider_secret":
		return "~", "will have its stored secret rotated"
	default:
		return "~", "will be updated"
	}
}

func planAddress(step provision.Step) string {
	kind := "app"
	switch {
	case step.Kind == "key_cap":
		kind = "cap"
	case strings.Contains(step.Kind, "redirect"):
		kind = "redirect_uri"
	case strings.HasPrefix(step.Kind, "key_"), step.Kind == "create_key", step.Kind == "revoke_key":
		kind = "inference_key"
	case strings.HasPrefix(step.Kind, "router_"), step.Kind == "create_router", step.Kind == "revoke_router":
		kind = "routing_key"
	case strings.HasPrefix(step.Kind, "service_"), step.Kind == "create_service":
		kind = "service_key"
	case strings.HasPrefix(step.Kind, "provider_"), step.Kind == "create_provider":
		kind = "provider_credential"
	}
	if kind == "app" {
		return kind
	}
	_, ref, found := strings.Cut(step.Ref, ":")
	if !found {
		ref = step.Ref
	}
	return kind + "[" + strconv.Quote(ref) + "]"
}

func writePlanFields(output *strings.Builder, step provision.Step, symbol string, color bool) {
	if symbol == "+" || step.After == nil {
		values := step.After
		if step.After == nil {
			values = step.Before
		}
		names := make([]string, 0, len(values))
		for name := range values {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			value := formatPlanValue(values[name])
			if symbol == "-" {
				value += " -> null"
			}
			writePlanField(output, symbol, name, value, color)
		}
		return
	}
	changes := step.FieldChanges()
	for _, field := range changes {
		value := formatPlanValue(field.Actual) + " -> " + formatPlanValue(field.Desired)
		writePlanField(output, symbol, field.Field, value, color)
	}
	if unchanged := len(step.After) - len(changes); unchanged > 0 {
		noun := "fields"
		if unchanged == 1 {
			noun = "field"
		}
		fmt.Fprintf(output, "      # %d unchanged %s hidden\n", unchanged, noun)
	}
}

func writePlanField(output *strings.Builder, symbol, name, value string, color bool) {
	switch name {
	case "app_name", "key_alias", "credential_name":
		name = "name"
	}
	value = strings.ReplaceAll(value, "\n", "\n      ")
	fmt.Fprintf(output, "    %s %s = %s\n", planSymbol(symbol, color), terminalText(name), value)
}

func formatPlanValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "(not set)"
	}
	compact, err := json.Marshal(raw)
	if err != nil {
		return "(unavailable)"
	}
	// Keep scalar values and short lists compact; expand objects and memberships.
	if len(compact) <= 80 && !strings.ContainsAny(string(compact), "{}") {
		return string(compact)
	}
	formatted, _ := json.MarshalIndent(raw, "", "  ")
	return string(formatted)
}

func planSymbol(symbol string, color bool) string {
	code := "33"
	if symbol == "+" {
		code = "32"
	} else if symbol == "-" {
		code = "31"
	}
	return planColor(symbol, code, color)
}

func planColor(text, code string, enabled bool) string {
	if !enabled {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func quotePlanPath(path string) string {
	path = terminalText(path)
	if !strings.ContainsAny(path, " \t\r\n'\"$`;&|<>(){}[]*?!\\#~@") {
		return path
	}
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(terminalText(path), "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(terminalText(path), "'", "'\"'\"'") + "'"
}
