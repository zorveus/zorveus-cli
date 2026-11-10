package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

type setupOptions struct {
	config, name, credits string
	models, plans         []string
	accepted, allowDelete bool
	defaults              *manifest.SaaSOptions
}

type setupResult struct {
	Config  string `json:"config"`
	Plan    string `json:"plan"`
	Applied bool   `json:"applied"`
	AppID   string `json:"app_id,omitempty"`
}

func setupCommand(settings *flags, dependencies Dependencies, stdout, stderr io.Writer) *cobra.Command {
	options := setupOptions{}
	command := &cobra.Command{
		Use: "setup", Short: "Set up an app and customer allowance plans with guided review",
		Args:              noArguments,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSetup(command, settings, dependencies, options, stdout, stderr)
		},
	}
	command.Flags().StringVar(&options.config, "config", "zorveus.json", "Manifest to create or reuse")
	command.Flags().StringVar(&options.name, "name", "", "New app display name")
	command.Flags().StringArrayVar(&options.models, "model", nil, "Allowed model ID for a new app (default: *)")
	command.Flags().StringVar(&options.credits, "credits-per-usd", "100", "Credit units per USD for a new app")
	command.Flags().StringArrayVar(&options.plans, "plan", nil, "reference=Display name:monthly USD allowance; repeat for more plans")
	command.Flags().BoolVar(&options.accepted, "yes", false, "Apply the reviewed configuration without another confirmation")
	command.Flags().BoolVar(&options.allowDelete, "allow-delete", false, "Also allow reviewed revocations and removals")
	return command
}

func runSetup(command *cobra.Command, settings *flags, dependencies Dependencies, options setupOptions, stdout, stderr io.Writer) error {
	interactive := dependencies.InputIsTerminal && !settings.nonInteractive
	if interactive && settings.jsonOutput {
		return problem.New("interactive_json_unsupported", "Use --non-interactive with explicit setup inputs for --json output.", 2)
	}
	info, err := os.Lstat(options.config)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) || exists && !info.Mode().IsRegular() {
		return problem.New("config_read_failed", "The setup manifest must be a regular file in a readable directory.", 1)
	}
	if exists {
		if _, err := manifest.Load(options.config); err != nil {
			return err
		}
		if options.name != "" || len(options.plans) > 0 || command.Flags().Changed("credits-per-usd") || command.Flags().Changed("model") {
			return problem.New("config_exists", "Setup preserves existing manifests. Edit their settings, then run setup again to review and apply the changes.", 2)
		}
	}
	var configuration manifest.SaaSOptions
	if !exists && !interactive {
		configuration, err = saasOptions(options.name, options.models, options.credits, options.plans)
		if err != nil {
			return err
		}
	}
	progress := stdout
	if settings.jsonOutput {
		progress = stderr
	}
	input := &setupInput{ctx: command.Context(), reader: bufio.NewReader(command.InOrStdin()), output: progress, color: terminalColors(settings, dependencies.OutputIsTerminal)}
	ui := input.ui()
	if err := ui.heading("Zorveus setup"); err != nil {
		return err
	}
	identity, err := setupLogin(command, settings, dependencies, interactive, stderr)
	if err != nil {
		return err
	}
	if err := ui.field("API", identity.APIOrigin); err != nil {
		return err
	}
	if err := ui.field("Organization", identity.Organization); err != nil {
		return err
	}
	settings.apiOrigin = identity.APIOrigin
	destination, err := setupDestination(input, &options, identity, exists, interactive)
	if err != nil {
		return err
	}
	if destination.Stop {
		return nil
	}
	flow := setupFlow{command: command, settings: settings, dependencies: dependencies, options: options, input: input, identity: identity, interactive: interactive, stdout: stdout, stderr: stderr, progress: progress}
	if !destination.Exists {
		proceed, err := flow.prepareManifest(configuration)
		if err != nil || !proceed {
			return err
		}
	}
	if destination.Exists && interactive && !destination.Review {
		review, err := flow.existingManifest()
		if err != nil || !review {
			return err
		}
	}
	return flow.reviewAndApply()
}

func setupLogin(command *cobra.Command, settings *flags, dependencies Dependencies, interactive bool, stderr io.Writer) (auth.WhoamiResult, error) {
	identity, err := dependencies.Auth.Whoami(command.Context(), settings.apiOrigin)
	if err == nil {
		return identity, nil
	}
	if !interactive || !errors.Is(err, auth.ErrNoLogin) {
		return identity, err
	}
	login := loginCommand(settings, dependencies, io.Discard, stderr)
	login.SetContext(command.Context())
	if err := login.RunE(login, nil); err != nil {
		return identity, err
	}
	return dependencies.Auth.Whoami(command.Context(), settings.apiOrigin)
}

func setupPlan(command *cobra.Command, service *provision.Service, origin, config string) (provision.PlanResult, error) {
	path := strings.TrimSuffix(config, filepath.Ext(config)) + ".plan.json"
	result, err := service.Plan(command.Context(), origin, config, path)
	if err == nil || problem.Safe(err).Code != "apply_pending" {
		return result, err
	}
	// Keep the original plan intact so interrupted provisioning can be resumed.
	data, readErr := os.ReadFile(path)
	var saved provision.Plan
	absolute, _ := filepath.Abs(config)
	if readErr != nil || json.Unmarshal(data, &saved) != nil || saved.Config != absolute {
		return result, err
	}
	return provision.PlanResult{Plan: path, Operations: len(saved.Steps), Changes: saved.Steps, RequiredScopes: saved.Scopes}, nil
}

func requireSetupTarget(path string, identity auth.WhoamiResult) error {
	data, err := os.ReadFile(path)
	var saved provision.Plan
	if err != nil || json.Unmarshal(data, &saved) != nil {
		return problem.New("invalid_plan", "Could not read the setup plan.", 2)
	}
	if saved.Target.Origin != identity.APIOrigin || saved.Target.Org != identity.Organization || saved.Target.User != identity.User.ID {
		return problem.New("setup_target_changed", "The saved login changed during setup. Run setup again to review the current API, organization, and user before applying.", 4)
	}
	return nil
}
