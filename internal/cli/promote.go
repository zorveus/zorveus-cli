package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func promoteCommand(settings *flags, stdout io.Writer) *cobra.Command {
	var source, target string
	var keepNames bool
	command := &cobra.Command{
		Use:               "promote",
		Short:             "Copy settings to an existing manifest, preserving target IDs",
		Args:              noArguments,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(command *cobra.Command, arguments []string) error {
			if source == "" || target == "" {
				return problem.New("invalid_arguments", "Provide both --from and --to for promote.", 2)
			}
			result, err := provision.Promote(command.Context(), source, target, keepNames)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			if !result.Changed {
				_, err = fmt.Fprintln(stdout, "Configuration already matches:", terminalText(target))
				return outputError(err)
			}
			preserved := "IDs"
			if keepNames {
				preserved = "IDs and display names"
			}
			_, err = fmt.Fprintf(stdout, "Updated %s with source settings, preserving existing target %s.\nReview it with zorveus plan --config %s\n", terminalText(target), preserved, quotePlanPath(target))
			return outputError(err)
		},
	}
	command.Flags().StringVar(&source, "from", "", "Source manifest supplying the desired configuration")
	command.Flags().StringVar(&target, "to", "", "Existing target manifest to update, keeping its resource IDs")
	command.Flags().BoolVar(&keepNames, "keep-names", false, "Preserve target display names instead of copying source names")
	return command
}
