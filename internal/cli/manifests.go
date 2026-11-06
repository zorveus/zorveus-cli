package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func initCommand(settings *flags, stdout io.Writer) *cobra.Command {
	var config string
	var name string
	var models []string
	command := &cobra.Command{
		Use:               "init",
		Short:             "Create a starter manifest without login or API requests",
		Args:              noArguments,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(command *cobra.Command, arguments []string) error {
			result, err := manifest.Init(config, name, models)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			_, err = fmt.Fprintln(stdout, "Created", terminalText(result.Config))
			return outputError(err)
		},
	}
	command.Flags().StringVar(&config, "config", "zorveus.json", "Destination manifest; must not already exist")
	command.Flags().StringVar(&name, "name", "My app", "App display name")
	command.Flags().StringArrayVar(&models, "model", []string{"*"}, "Allowed model ID; repeat for multiple models")
	return command
}

func validateCommand(settings *flags, stdout, stderr io.Writer) *cobra.Command {
	var config string
	command := &cobra.Command{
		Use:               "validate",
		Short:             "Validate a manifest offline and report checks that need remote state",
		Args:              noArguments,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(command *cobra.Command, arguments []string) error {
			document, err := manifest.Load(config)
			if err != nil {
				return err
			}
			result := document.Result(config)
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			for _, check := range result.DeferredChecks {
				_, _ = fmt.Fprintf(stderr, "Deferred at %s: %s\n", terminalText(check.Pointer), check.Message)
			}
			_, err = fmt.Fprintln(stdout, "Valid manifest:", terminalText(config))
			return outputError(err)
		},
	}
	command.Flags().StringVar(&config, "config", "zorveus.json", "Manifest to validate")
	return command
}

func cloneCommand(settings *flags, stdout io.Writer) *cobra.Command {
	var source string
	var destination string
	var name string
	command := &cobra.Command{
		Use:               "clone",
		Short:             "Copy a manifest and remove app-owned resource IDs, entirely offline",
		Args:              noArguments,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(command *cobra.Command, arguments []string) error {
			if source == "" || destination == "" {
				return problem.New("invalid_arguments", "Provide both --from and --to for clone.", 2)
			}
			if command.Flags().Changed("name") && name == "" {
				return problem.New("invalid_arguments", "The replacement app name must not be empty.", 2)
			}
			result, err := manifest.Clone(source, destination, name)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			_, err = fmt.Fprintln(stdout, "Created", terminalText(result.Config))
			return outputError(err)
		},
	}
	command.Flags().StringVar(&source, "from", "", "Source manifest")
	command.Flags().StringVar(&destination, "to", "", "Destination manifest; must not already exist")
	command.Flags().StringVar(&name, "name", "", "Replacement app display name")
	return command
}
