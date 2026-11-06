package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func importCommand(settings *flags, dependencies Dependencies, stdout io.Writer) *cobra.Command {
	var appID, config string
	command := &cobra.Command{
		Use: "import", Short: "Read an existing app into a new manifest", Args: noArguments,
		RunE: func(command *cobra.Command, arguments []string) error {
			service := provision.Service{Auth: dependencies.Auth}
			result, err := service.Import(command.Context(), settings.apiOrigin, appID, config)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			_, err = fmt.Fprintf(stdout, "Imported app %s to %s with %d keys and %d routers.\n", terminalText(result.AppID), terminalText(result.Config), result.Keys, result.Routers)
			return outputError(err)
		},
	}
	command.Flags().StringVar(&appID, "app-id", "", "Existing app to import from the selected organization")
	command.Flags().StringVar(&config, "config", "zorveus.json", "New manifest path; existing files are refused")
	_ = command.MarkFlagFilename("config", "json")
	return command
}

func statusCommand(settings *flags, dependencies Dependencies, stdout io.Writer) *cobra.Command {
	var config string
	command := &cobra.Command{
		Use: "status", Short: "Read resource state, configuration drift, and the last apply result", Args: noArguments,
		RunE: func(command *cobra.Command, arguments []string) error {
			service := provision.Service{Auth: dependencies.Auth}
			result, err := service.Status(command.Context(), settings.apiOrigin, config)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			for _, resource := range result.Resources {
				comparison := "matches"
				if !resource.Matches {
					comparison = "differs"
				}
				if _, err := fmt.Fprintf(stdout, "%s %s: %s, %s\n", terminalText(resource.Ref), terminalText(resource.ID), terminalText(resource.Status), comparison); err != nil {
					return outputError(err)
				}
				for _, change := range resource.Changes {
					if _, err := fmt.Fprintf(stdout, "  %s: remote %s, manifest %s\n", terminalText(change.Field), change.Actual, change.Desired); err != nil {
						return outputError(err)
					}
				}
				if resource.SecretStatus != "" {
					if _, err := fmt.Fprintln(stdout, "  provider input:", terminalText(resource.SecretStatus)); err != nil {
						return outputError(err)
					}
				}
				for _, note := range resource.Notes {
					if _, err := fmt.Fprintln(stdout, " ", terminalText(note)); err != nil {
						return outputError(err)
					}
				}
			}
			if result.LastApply != nil {
				last := result.LastApply
				if _, err := fmt.Fprintf(stdout, "Last apply %s: complete=%t, %d/%d steps\n", terminalText(last.Operation), last.Complete, last.Completed, last.Total); err != nil {
					return outputError(err)
				}
			}
			_, err = fmt.Fprintf(stdout, "In sync: %t\n", result.InSync)
			return outputError(err)
		},
	}
	command.Flags().StringVar(&config, "config", "zorveus.json", "Manifest to inspect")
	_ = command.MarkFlagFilename("config", "json")
	return command
}
