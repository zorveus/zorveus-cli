package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func serviceKeysCommand(settings *flags, dependencies Dependencies, stdout io.Writer) *cobra.Command {
	service := provision.Service{Auth: dependencies.Auth}
	command := &cobra.Command{Use: "service-keys", Short: "Inspect and explicitly revoke organization service keys", ValidArgsFunction: cobra.NoFileCompletions}
	list := &cobra.Command{
		Use: "list", Short: "List service-key metadata without secret values", Args: noArguments,
		RunE: func(command *cobra.Command, arguments []string) error {
			result, err := service.ServiceKeys(command.Context(), settings.apiOrigin)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			for _, key := range result.Keys {
				name := ""
				if key.Name != nil {
					name = *key.Name
				}
				if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\n", terminalText(key.ID), terminalText(name), terminalText(key.Status)); err != nil {
					return outputError(err)
				}
			}
			return nil
		},
	}
	var id string
	var accepted bool
	revoke := &cobra.Command{
		Use: "revoke", Short: "Revoke a service key for the whole organization", Args: noArguments,
		RunE: func(command *cobra.Command, arguments []string) error {
			result, err := service.RevokeServiceKey(command.Context(), settings.apiOrigin, id, accepted)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			_, err = fmt.Fprintln(stdout, "Revoked service key", terminalText(result.ID))
			return outputError(err)
		},
	}
	revoke.Flags().StringVar(&id, "id", "", "Service-key ID in the selected organization")
	revoke.Flags().BoolVar(&accepted, "yes", false, "Confirm organization-wide revocation")
	command.AddCommand(list, revoke)
	return command
}
