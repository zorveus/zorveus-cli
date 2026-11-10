package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

type setupFlow struct {
	command      *cobra.Command
	settings     *flags
	dependencies Dependencies
	options      setupOptions
	input        *setupInput
	identity     auth.WhoamiResult
	interactive  bool
	stdout       io.Writer
	stderr       io.Writer
	progress     io.Writer
}

func (flow *setupFlow) prepareManifest(configuration manifest.SaaSOptions) (bool, error) {
	if flow.interactive {
		var err error
		configuration, err = collectSetup(flow.input, flow.options)
		if err != nil {
			return false, err
		}
		if !flow.options.accepted {
			proceed, err := flow.input.confirm("Save and review plan?", true)
			if err != nil {
				return false, err
			}
			if !proceed {
				flow.input.ui().note("Canceled. No files saved or resources created.")
				return false, nil
			}
		}
	}
	_, err := manifest.InitSaaS(flow.options.config, configuration)
	return err == nil, err
}

func (flow *setupFlow) reviewAndApply() error {
	service := provision.Service{Auth: flow.dependencies.Auth, Secrets: flow.dependencies.Secrets}
	plan, err := setupPlan(flow.command, &service, flow.identity.APIOrigin, flow.options.config)
	if err != nil && flow.interactive && problem.Safe(err).Code == "authorization_required" {
		if err := flow.reauthorize(err); err != nil {
			return err
		}
		plan, err = setupPlan(flow.command, &service, flow.identity.APIOrigin, flow.options.config)
	}
	if err != nil {
		return err
	}
	if err := requireSetupTarget(plan.Plan, flow.identity); err != nil {
		return err
	}
	if _, err := io.WriteString(flow.progress, formatSetupPlan(plan, flow.input.color)); err != nil {
		return outputError(err)
	}
	accepted := flow.options.accepted || plan.Operations == 0
	if flow.interactive && !accepted {
		accepted, err = flow.input.confirm(setupApplyQuestion(plan), false)
		if err != nil {
			return err
		}
	}
	result := setupResult{Config: flow.options.config, Plan: plan.Plan}
	if !accepted {
		if flow.settings.jsonOutput {
			return writeJSON(flow.stdout, flow.command.CommandPath(), result)
		}
		flow.input.ui().note("Plan saved. No changes applied.")
		command := "zorveus apply --plan " + quotePlanPath(plan.Plan) + " --yes"
		for _, step := range plan.Changes {
			if step.Destructive {
				command += " --allow-delete"
				break
			}
		}
		flow.input.ui().note("Apply later:")
		_, err := fmt.Fprintf(flow.progress, "  %s\n", flow.input.ui().text(command, terminalAccent))
		return outputError(err)
	}
	target := auth.LoginTarget{APIOrigin: flow.identity.APIOrigin, Organization: flow.identity.Organization, UserID: flow.identity.User.ID}
	permissionErr := flow.dependencies.Auth.UseLogin(flow.input.ctx, target, plan.RequiredScopes, func(auth.Login) error { return nil })
	if permissionErr != nil {
		if err := flow.reauthorize(permissionErr); err != nil {
			return err
		}
	}
	// Apply validates the saved target again, including the organization and user.
	applied, err := service.Apply(flow.input.ctx, flow.identity.APIOrigin, plan.Plan, true, flow.options.allowDelete)
	if err != nil {
		return err
	}
	result.Applied, result.AppID = true, applied.AppID
	if flow.settings.jsonOutput {
		return writeJSON(flow.stdout, flow.command.CommandPath(), result)
	}
	flow.input.ui().success("Setup complete")
	flow.input.ui().field("App", applied.AppID)
	if len(applied.CredentialAccounts) > 0 {
		flow.input.ui().note("Credentials saved securely.")
	}
	return nil
}

func (flow *setupFlow) reauthorize(cause error) error {
	code := problem.Safe(cause).Code
	if !flow.interactive || code != "authorization_required" {
		return cause
	}
	flow.input.ui().warning(problem.Safe(cause).Message)
	approved, err := flow.input.confirm("Sign in again?", true)
	if err != nil {
		return err
	}
	if !approved {
		return cause
	}
	login := loginCommand(flow.settings, flow.dependencies, io.Discard, flow.stderr)
	login.SetContext(flow.input.ctx)
	if err := login.RunE(login, nil); err != nil {
		return err
	}
	current, err := flow.dependencies.Auth.Whoami(flow.input.ctx, flow.identity.APIOrigin)
	if err != nil {
		return err
	}
	if current.Organization != flow.identity.Organization || current.User.ID != flow.identity.User.ID {
		return problem.New("setup_target_changed", "The login selected another organization or user. Run setup again to review that target before applying.", 4)
	}
	return nil
}
