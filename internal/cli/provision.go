package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func planCommand(settings *flags, dependencies Dependencies, stdout io.Writer) *cobra.Command {
	var config, output, source string
	var keepNames bool
	command := &cobra.Command{
		Use:   "plan",
		Short: "Read remote resources and save proposed manifest changes",
		Args:  noArguments,
		RunE: func(command *cobra.Command, arguments []string) error {
			if !command.Flags().Changed("out") {
				output = strings.TrimSuffix(config, filepath.Ext(config)) + ".plan.json"
			}
			service := provision.Service{Auth: dependencies.Auth, Secrets: dependencies.Secrets}
			result, err := service.PlanFrom(command.Context(), settings.apiOrigin, config, output, source, keepNames)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			color := terminalColors(settings, dependencies.OutputIsTerminal)
			_, err = io.WriteString(stdout, formatPlan(result, color))
			return outputError(err)
		},
	}
	command.Flags().StringVar(&config, "config", "zorveus.json", "Manifest to compare")
	command.Flags().StringVar(&source, "from", "", "Use settings from this manifest, preserving existing target IDs")
	command.Flags().BoolVar(&keepNames, "keep-names", false, "With --from, preserve target display names instead of copying source names")
	command.Flags().StringVar(&output, "out", "", "Saved plan path; defaults to <manifest-name>.plan.json beside the manifest")
	_ = command.MarkFlagFilename("config", "json")
	_ = command.MarkFlagFilename("from", "json")
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
			if len(result.CredentialAccounts) > 0 {
				if _, err := fmt.Fprintln(stdout, "Secrets saved in your OS credential store. Use zorveus retrieve to read one."); err != nil {
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
