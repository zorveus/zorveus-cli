package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

func whoamiCommand(settings *flags, dependencies Dependencies, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:               "whoami",
		Short:             "Verify the current user and session through /me",
		Args:              noArguments,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(command *cobra.Command, arguments []string) error {
			result, err := dependencies.Auth.Whoami(command.Context(), settings.apiOrigin)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			_, err = fmt.Fprintf(stdout, "User: %s\nEmail: %s\nOrganization: %s\nSession expires: %s\n",
				terminalText(result.User.ID), terminalText(result.User.Email), terminalText(result.Organization),
				result.Session.ExpiresAt.Format(time.RFC3339))
			return outputError(err)
		},
	}
}
