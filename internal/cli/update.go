package cli

import (
	"context"
	"io"
	"time"

	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/selfupdate"
)

func updateCommand(settings *flags, dependencies Dependencies, stdout, stderr io.Writer) *cobra.Command {
	var version string
	command := &cobra.Command{
		Use: "update", Short: "Install the latest published CLI release in place", Args: noArguments,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(command *cobra.Command, arguments []string) error {
			update := dependencies.UpdateCLI
			if update == nil {
				update = selfupdate.New().Run
			}
			diagnostics := terminalUI{output: stderr, color: terminalColors(settings, dependencies.ErrorIsTerminal)}
			if err := diagnostics.note("Checking Zorveus release..."); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(command.Context(), 10*time.Minute)
			defer cancel()
			result, err := update(ctx, version)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			ui := terminalUI{output: stdout, color: terminalColors(settings, dependencies.OutputIsTerminal)}
			message := "Updated Zorveus to " + result.Version
			if result.AlreadyCurrent {
				message = "Zorveus " + result.Version + " is already installed"
			}
			if err := ui.success(message); err != nil {
				return err
			}
			return ui.field("Executable", result.Executable)
		},
	}
	command.Flags().StringVar(&version, "version", "", "Install a specific release, such as v0.1.0, instead of the latest stable release")
	return command
}
