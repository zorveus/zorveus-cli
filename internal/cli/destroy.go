package cli

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func destroyCommand(settings *flags, dependencies Dependencies, stdout, stderr io.Writer) *cobra.Command {
	var config, output string
	var accepted, dryRun bool
	command := &cobra.Command{
		Use: "destroy", Short: "Delete the app and revoke its keys and all listed organization credentials", Args: noArguments,
		RunE: func(command *cobra.Command, _ []string) error {
			if !command.Flags().Changed("out") {
				output = strings.TrimSuffix(config, filepath.Ext(config)) + ".destroy.plan.json"
			}
			service := provisionService(settings, dependencies)
			plan, err := service.PlanDestroy(command.Context(), settings.apiOrigin, config, output)
			if err != nil {
				return err
			}
			if settings.jsonOutput && (dryRun || plan.Operations == 0) {
				return writeJSON(stdout, command.CommandPath(), plan)
			}
			if !settings.jsonOutput {
				ui := terminalUI{output: stdout, color: terminalColors(settings, dependencies.OutputIsTerminal)}
				if err := ui.field("API", plan.Target.Origin); err != nil {
					return err
				}
				if err := ui.field("Organization", plan.Target.Org); err != nil {
					return err
				}
				if plan.Operations > 0 {
					if err := ui.warning("App deletion revokes all its connections, including customer connections. Listed service keys and provider credentials are also removed and may be shared with other apps."); err != nil {
						return err
					}
				}
				if _, err := io.WriteString(stdout, formatPlan(plan, ui.color)); err != nil {
					return outputError(err)
				}
			}
			if dryRun || plan.Operations == 0 {
				return nil
			}
			if !accepted && dependencies.InputIsTerminal && !settings.nonInteractive && !settings.jsonOutput {
				input := setupInput{ctx: command.Context(), reader: bufio.NewReader(command.InOrStdin()), output: stderr, color: terminalColors(settings, dependencies.OutputIsTerminal)}
				accepted, err = input.confirm("Delete this app and revoke all credentials shown above in your Zorveus account?", false)
				if err != nil {
					if problem.Safe(err).Code == "setup_canceled" {
						return problem.New("destroy_canceled", "Destroy canceled before any resources were removed.", 130)
					}
					return err
				}
				if !accepted {
					_, err := fmt.Fprintln(stdout, "Destroy canceled. The saved plan is available for review.")
					return outputError(err)
				}
			}
			if !accepted {
				return problem.New("acceptance_required", "Review destroy --dry-run, then pass --yes to destroy these resources.", 2)
			}
			result, err := service.Apply(command.Context(), settings.apiOrigin, output, true, true)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			_, err = fmt.Fprintf(stdout, "Destroyed deployment. App: %s\nManifest and tracking retained: %s\n", terminalText(result.AppID), terminalText(result.Config))
			return outputError(err)
		},
	}
	command.Flags().StringVar(&config, "config", "zorveus.json", "Manifest identifying the app and credentials to destroy")
	command.Flags().StringVar(&output, "out", "", "Saved destroy plan; defaults to <manifest-name>.destroy.plan.json")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "Save and show the destroy plan without deleting resources")
	command.Flags().BoolVar(&accepted, "yes", false, "Confirm deletion of the app and all credentials shown in the destroy plan")
	_ = command.MarkFlagFilename("config", "json")
	_ = command.MarkFlagFilename("out", "json")
	return command
}
