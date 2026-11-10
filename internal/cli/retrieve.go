package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func retrieveCommand(settings *flags, dependencies Dependencies, stdout io.Writer) *cobra.Command {
	var config string
	command := &cobra.Command{
		Use:   "retrieve <item-or-id>",
		Short: "Print one saved secret by manifest reference or resource ID",
		Example: "  zorveus retrieve keys.plus\n" +
			"  zorveus retrieve app --config zorveus.prod.json\n" +
			"  zorveus retrieve serviceKeys.customers\n" +
			"  zorveus retrieve aconn_example",
		Args: func(command *cobra.Command, arguments []string) error {
			if len(arguments) != 1 {
				return problem.New("invalid_arguments", "Choose one item: app, keys.<name>, routingKeys.<name>, serviceKeys.<name>, or an ID from the manifest.", 2)
			}
			return nil
		},
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(command *cobra.Command, arguments []string) error {
			service := provision.Service{Auth: dependencies.Auth, Secrets: dependencies.Secrets}
			result, err := service.Retrieve(command.Context(), settings.apiOrigin, config, arguments[0])
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			// Keep stdout to the requested value so it can be copied or piped.
			_, err = fmt.Fprintln(stdout, result.Secret)
			return outputError(err)
		},
	}
	command.Flags().StringVar(&config, "config", "zorveus.json", "Manifest containing the item or resource ID")
	_ = command.MarkFlagFilename("config", "json")
	return command
}
