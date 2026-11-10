package cli

import (
	"slices"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/manifest"
)

func collectSetupRouting(input *setupInput, flags setupOptions, options manifest.SaaSOptions, existing *manifest.SaaSRouting) (manifest.SaaSOptions, error) {
	ui := input.ui()
	question := "Create a routing key?"
	if existing != nil {
		question = "Update routing key to include all plans?"
	}
	if !flags.routingKey {
		approved, err := input.confirm(question, false)
		if err != nil {
			return options, err
		}
		if !approved {
			return options, nil
		}
	}
	members := make([]string, 0, len(options.Plans))
	for _, plan := range options.Plans {
		members = append(members, plan.Reference)
	}
	ui.heading("Routing key")
	ui.field("Selectors", strings.Join(members, ", "))
	name, selector := options.Name+" router", members[0]
	if existing != nil {
		if existing.Name != "" {
			name = existing.Name
		}
		if slices.Contains(members, existing.DefaultSelector) {
			selector = existing.DefaultSelector
		}
	}
	if flags.defaultSelector != "" {
		selector = flags.defaultSelector
	}
	var err error
	name, err = input.ask("Routing key name", name)
	if err != nil {
		return options, err
	}
	for {
		selected, err := input.ask("Default selector", selector)
		if err != nil {
			return options, err
		}
		if !slices.Contains(members, selected) {
			ui.warning("Choose one of: " + strings.Join(members, ", "))
			continue
		}
		options.Routing = &manifest.SaaSRouting{Name: name, DefaultSelector: selected}
		return options, nil
	}
}
