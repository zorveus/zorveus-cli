package cli

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

type setupDestinationResult struct {
	Exists bool
	Review bool
	Stop   bool
}

// Recovery chooses an existing app or resets local tracking. It never creates
// another manifest just to get past stale state.
func setupDestination(input *setupInput, options *setupOptions, identity auth.WhoamiResult, exists, interactive bool) (setupDestinationResult, error) {
	current := setupDestinationResult{Exists: exists}
	appID := ""
	var expected []byte
	if exists {
		document, err := manifest.Load(options.config)
		if err != nil {
			return current, err
		}
		appID = document.AppID()
		expected, err = document.Bytes()
		if err != nil {
			return current, err
		}
	}
	target := provision.Target{Origin: identity.APIOrigin, Org: identity.Organization, User: identity.User.ID}
	err := provision.CheckSetupState(options.config, target, appID)
	if err == nil || exists && problem.Safe(err).Code == "apply_pending" {
		return current, nil
	}
	if problem.Safe(err).Code != "state_target_mismatch" {
		return current, err
	}
	if !interactive {
		return current, problem.New("state_target_mismatch", "Saved tracking conflicts with this configuration. Run setup interactively to select an existing app or start fresh.", 4)
	}
	configurations, err := provision.FindSetupConfigurations(options.config, target)
	if err != nil {
		return current, err
	}
	currentAction := "Start a new app"
	if appID != "" {
		currentAction = "Use the app in the current configuration"
	}
	ui := input.ui()
	if len(configurations) == 0 {
		ui.note("This folder has previous setup tracking.")
		ui.note("Previous tracking will be backed up. Existing apps stay active.")
		proceed, err := input.confirm(currentAction+"?", false)
		if err != nil {
			return current, err
		}
		if !proceed {
			ui.note("Canceled. Existing configuration kept.")
			return setupDestinationResult{Stop: true}, nil
		}
	} else {
		ui.heading("Saved setups")
		ui.note("Restore a saved setup to " + filepath.Base(options.config) + ". Current files will be backed up.")
		for index, saved := range configurations {
			name := saved.Name
			if name == "" {
				name = saved.AppID
			}
			_, _ = fmt.Fprintf(input.output, "  %s %s %s\n", ui.text(strconv.Itoa(index+1), terminalAccent), ui.text(name, terminalBold), ui.text("("+filepath.Base(saved.Config)+")", terminalMuted))
			ui.note(saved.AppID)
		}
		newChoice, cancelChoice := len(configurations)+1, len(configurations)+2
		_, _ = fmt.Fprintf(input.output, "  %d %s\n  %d Cancel\n", newChoice, currentAction, cancelChoice)
		fallback := ""
		if len(configurations) == 1 {
			fallback = "1"
		}
		if appID != "" || options.name != "" || len(options.models) > 0 || len(options.plans) > 0 {
			fallback = strconv.Itoa(newChoice)
		}
		for {
			answer, err := input.ask("Choose setup", fallback)
			if err != nil {
				return current, err
			}
			choice, err := strconv.Atoi(answer)
			if err != nil || choice < 1 || choice > cancelChoice {
				ui.warning("Choose a number from the list.")
				continue
			}
			if choice == cancelChoice {
				ui.note("Canceled. Existing configuration kept.")
				return setupDestinationResult{Stop: true}, nil
			}
			if choice <= len(configurations) {
				if _, err := provision.UseSetupConfiguration(input.ctx, configurations[choice-1], options.config, target, expected); err != nil {
					return current, err
				}
				ui.field("Config", options.config)
				return setupDestinationResult{Exists: true, Review: true}, nil
			}
			ui.note("Previous tracking will be backed up. Existing apps stay active.")
			break
		}
	}
	if _, err := provision.ArchiveSetupState(input.ctx, options.config, target, appID); err != nil {
		return current, err
	}
	ui.success("Previous local tracking backed up.")
	return setupDestinationResult{Exists: exists, Review: exists}, nil
}
