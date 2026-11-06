package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func planCommand(settings *flags, dependencies Dependencies, stdout io.Writer) *cobra.Command {
	var config, output string
	command := &cobra.Command{
		Use:   "plan",
		Short: "Read remote resources and save proposed manifest changes",
		Args:  noArguments,
		RunE: func(command *cobra.Command, arguments []string) error {
			service := provision.Service{Auth: dependencies.Auth, Secrets: dependencies.Secrets}
			result, err := service.Plan(command.Context(), settings.apiOrigin, config, output)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			for _, change := range result.Changes {
				before, _ := json.Marshal(change.Before)
				after, _ := json.Marshal(change.After)
				if _, err := fmt.Fprintf(stdout, "%s %s\n  before: %s\n  after:  %s\n", terminalText(change.Kind), terminalText(change.Ref), before, after); err != nil {
					return outputError(err)
				}
			}
			_, err = fmt.Fprintf(stdout, "%d proposed changes. Saved plan to %s\n", result.Operations, terminalText(result.Plan))
			return outputError(err)
		},
	}
	command.Flags().StringVar(&config, "config", "zorveus.json", "Manifest to compare")
	command.Flags().StringVar(&output, "out", "zorveus.plan.json", "New file for the saved plan")
	_ = command.MarkFlagFilename("config", "json")
	_ = command.MarkFlagFilename("out", "json")
	return command
}

func applyCommand(settings *flags, dependencies Dependencies, stdout io.Writer) *cobra.Command {
	var plan string
	var accepted, allowDelete bool
	command := &cobra.Command{
		Use:   "apply",
		Short: "Apply a reviewed saved plan and record resource IDs",
		Args:  noArguments,
		RunE: func(command *cobra.Command, arguments []string) error {
			service := provision.Service{Auth: dependencies.Auth, Secrets: dependencies.Secrets}
			result, err := service.Apply(command.Context(), settings.apiOrigin, plan, accepted, allowDelete)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			if _, err := fmt.Fprintf(stdout, "Applied plan. App: %s\nUpdated manifest: %s\n", terminalText(result.AppID), terminalText(result.Config)); err != nil {
				return outputError(err)
			}
			for _, account := range result.CredentialAccounts {
				if _, err := fmt.Fprintf(stdout, "Credential saved in OS credential store: %s\n", terminalText(account)); err != nil {
					return outputError(err)
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&plan, "plan", "zorveus.plan.json", "Saved plan to apply or resume")
	command.Flags().BoolVar(&accepted, "yes", false, "Confirm the reviewed changes")
	command.Flags().BoolVar(&allowDelete, "allow-delete", false, "Also allow planned revocations and cap removal")
	_ = command.MarkFlagFilename("plan", "json")
	return command
}
