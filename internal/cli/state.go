package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func provisionService(settings *flags, dependencies Dependencies) provision.Service {
	return provision.Service{Auth: dependencies.Auth, Secrets: dependencies.Secrets, StateConfig: settings.stateBackend, BackendFactory: dependencies.StateBackendFactory}
}

func stateCommand(settings *flags, dependencies Dependencies, stdout io.Writer) *cobra.Command {
	var config string
	group := &cobra.Command{Use: "state", Short: "Initialize, inspect, back up, and unlock durable state", ValidArgsFunction: cobra.NoFileCompletions}
	group.PersistentFlags().StringVar(&config, "config", "zorveus.json", "Manifest selecting this app's state backend")
	var copyLocal, force bool
	initialize := &cobra.Command{Use: "init", Short: "Initialize or reconnect to durable state without overwriting it", Args: noArguments,
		RunE: func(command *cobra.Command, _ []string) error {
			service := provisionService(settings, dependencies)
			result, err := service.InitializeState(command.Context(), config, copyLocal, force)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			if result.BackupPath != "" {
				if _, err := fmt.Fprintf(stdout, "Previous local tracking backed up: %s\n", terminalText(result.BackupPath)); err != nil {
					return outputError(err)
				}
			}
			message := "Initialized durable state"
			if result.AlreadyInitialized {
				message = "Durable state already initialized"
			}
			_, err = fmt.Fprintf(stdout, "%s: %s\n", message, terminalText(result.ID))
			return outputError(err)
		}}
	initialize.Flags().BoolVar(&copyLocal, "copy-local", false, "Copy reviewed local tracking for an already applied manifest into a new backend")
	initialize.Flags().BoolVar(&force, "force", false, "Back up this manifest's local state and apply journals, then initialize fresh state; never overwrite existing durable state")
	inspect := &cobra.Command{Use: "inspect", Short: "Show the durable state identity, revision, app, and lock", Args: noArguments,
		RunE: func(command *cobra.Command, _ []string) error {
			service := provisionService(settings, dependencies)
			result, err := service.InspectState(command.Context(), config)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			ui := terminalUI{output: stdout, color: terminalColors(settings, dependencies.OutputIsTerminal)}
			if err := ui.field("State", result.ID); err != nil {
				return err
			}
			if err := ui.field("Revision", fmt.Sprint(result.Revision)); err != nil {
				return err
			}
			if result.AppID != "" {
				if err := ui.field("App", result.AppID); err != nil {
					return err
				}
			}
			if result.Destroyed {
				if err := ui.field("Deployment", "destroyed"); err != nil {
					return err
				}
			}
			if result.Operation != "" {
				if err := ui.field("Last apply", fmt.Sprintf("%s, complete=%t", result.Operation, result.ApplyComplete)); err != nil {
					return err
				}
			}
			if result.Lock != nil {
				if err := ui.field("Lock ID", result.Lock.ID); err != nil {
					return err
				}
				return ui.field("Owner", result.Lock.Owner)
			}
			return ui.note("State is unlocked.")
		}}
	var lockID string
	var accepted bool
	unlock := &cobra.Command{Use: "unlock", Short: "Release an exact lock only after confirming its deployment has stopped", Args: noArguments,
		RunE: func(command *cobra.Command, _ []string) error {
			service := provisionService(settings, dependencies)
			if err := service.UnlockState(command.Context(), config, lockID, accepted); err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), map[string]bool{"unlocked": true})
			}
			_, err := fmt.Fprintln(stdout, "Durable state unlocked.")
			return outputError(err)
		}}
	unlock.Flags().StringVar(&lockID, "lock-id", "", "Exact lock ID shown by state inspect")
	unlock.Flags().BoolVar(&accepted, "yes", false, "Confirm the previous deployment has stopped and cannot send further writes")
	var output string
	pull := &cobra.Command{Use: "pull", Short: "Save a state snapshot backup without overwriting an existing file", Args: noArguments,
		RunE: func(command *cobra.Command, _ []string) error {
			service := provisionService(settings, dependencies)
			result, err := service.PullState(command.Context(), config, output)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			_, err = fmt.Fprintln(stdout, "Saved state snapshot:", terminalText(output))
			return outputError(err)
		}}
	pull.Flags().StringVar(&output, "out", "zorveus.state.backup.json", "New backup file; contains IDs, policies, and apply progress, but no login tokens or generated secrets")
	group.AddCommand(initialize, inspect, unlock, pull)
	return group
}
