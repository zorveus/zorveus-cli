package cli

import (
	"fmt"
	"io"
	"os"

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
			color := dependencies.OutputIsTerminal && !settings.noColor && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
			_, err = io.WriteString(stdout, formatPlan(result, color))
			return outputError(err)
		},
	}
	command.Flags().StringVar(&config, "config", "zorveus.json", "Manifest to compare")
	command.Flags().StringVar(&output, "out", "zorveus.plan.json", "Saved plan path; replaces an existing plan for this manifest")
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
