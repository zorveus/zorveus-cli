package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/provision"
	"github.com/zorveus/zorveus-cli/internal/selfupdate"
)

type Dependencies struct {
	Auth             *auth.Service
	Secrets          provision.SecretStore
	Stdin            io.Reader
	InputIsTerminal  bool
	OutputIsTerminal bool
	ErrorIsTerminal  bool
	OpenBrowser      func(context.Context, string) error
	UpdateCLI        func(context.Context, string) (selfupdate.Result, error)
}

const defaultLoginScopes = "apps:read apps:create apps:update apps:delete apps:rotate_secret " +
	"app_connections:read app_connections:write app_connections:revoke " +
	"routing_keys:read routing_keys:write " +
	"caps:read caps:app_write caps:org_write " +
	"service_keys:read service_keys:write service_keys:revoke " +
	"provider_credentials:read provider_credentials:write"

type flags struct {
	jsonOutput         bool
	nonInteractive     bool
	localMode          bool
	allowInsecureLocal bool
	apiOrigin          string
	logLevel           string
	noColor            bool
}

type resultEnvelope struct {
	Version int            `json:"version"`
	OK      bool           `json:"ok"`
	Command string         `json:"command"`
	Result  any            `json:"result,omitempty"`
	Error   *problem.Error `json:"error,omitempty"`
}

func Execute(ctx context.Context, arguments []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	// Browser recovery belongs to this invocation, never to a shared MCP service.
	if dependencies.Auth != nil {
		service := *dependencies.Auth
		service.RequestLogin = nil
		dependencies.Auth = &service
	}
	settings := &flags{}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	root := &cobra.Command{
		Use:               "zorveus",
		Short:             "Manage Zorveus from your terminal",
		SilenceUsage:      true,
		SilenceErrors:     true,
		ValidArgsFunction: cobra.NoFileCompletions,
		PersistentPreRunE: func(command *cobra.Command, arguments []string) error {
			if dependencies.Auth != nil && dependencies.InputIsTerminal && !settings.nonInteractive && !isMCPCommand(command) {
				input := command.InOrStdin()
				if command.Name() == "setup" {
					// Setup and sign-in prompts must share any input already buffered.
					input = bufio.NewReader(input)
					command.Root().SetIn(input)
				}
				dependencies.Auth.RequestLogin = loginRecovery(settings, dependencies, input, stderr)
			}
			level := new(slog.Level)
			if err := level.UnmarshalText([]byte(settings.logLevel)); err != nil {
				return problem.New("invalid_log_level", "Use --log-level debug, info, warn, or error.", 2)
			}
			logger = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: *level}))
			if settings.localMode {
				settings.allowInsecureLocal = true
				if settings.apiOrigin == "" {
					settings.apiOrigin = "http://localhost:8000"
				}
			}
			if settings.apiOrigin == "" {
				return nil
			}
			origin, err := api.CanonicalOrigin(settings.apiOrigin, settings.allowInsecureLocal)
			if err != nil {
				return err
			}
			settings.apiOrigin = origin
			return nil
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetIn(dependencies.Stdin)
	root.SetArgs(arguments)
	root.SetFlagErrorFunc(func(*cobra.Command, error) error {
		return problem.New("invalid_arguments", "Invalid flags. Run zorveus --help for usage.", 2)
	})
	root.PersistentFlags().BoolVar(&settings.jsonOutput, "json", false, "Write one versioned JSON result")
	root.PersistentFlags().BoolVar(&settings.noColor, "no-color", false, "Disable terminal colors")
	root.PersistentFlags().BoolVar(&settings.nonInteractive, "non-interactive", false, "Never open a browser or wait for input")
	root.PersistentFlags().BoolVar(&settings.localMode, "local", false, "Use the local API on port 8000 and dashboard on port 3000")
	root.PersistentFlags().BoolVar(&settings.allowInsecureLocal, "allow-insecure-loopback", false, "Allow HTTP origins on localhost or loopback IPs for local development")
	root.PersistentFlags().StringVar(&settings.apiOrigin, "api-url", "", "Override the API origin; login defaults to https://api.zorveus.com")
	root.PersistentFlags().StringVar(&settings.logLevel, "log-level", "info", "Diagnostic level: debug, info, warn, error")
	root.AddCommand(loginCommand(settings, dependencies, stdout, stderr))
	root.AddCommand(appsCommand(settings, dependencies, stdout))
	root.AddCommand(whoamiCommand(settings, dependencies, stdout))
	root.AddCommand(logoutCommand(settings, dependencies, stdout))
	root.AddCommand(initCommand(settings, stdout))
	root.AddCommand(setupCommand(settings, dependencies, stdout, stderr))
	root.AddCommand(retrieveCommand(settings, dependencies, stdout))
	root.AddCommand(validateCommand(settings, stdout, stderr))
	root.AddCommand(cloneCommand(settings, stdout))
	root.AddCommand(promoteCommand(settings, stdout))
	root.AddCommand(planCommand(settings, dependencies, stdout))
	root.AddCommand(applyCommand(settings, dependencies, stdout))
	root.AddCommand(importCommand(settings, dependencies, stdout))
	root.AddCommand(statusCommand(settings, dependencies, stdout))
	root.AddCommand(serviceKeysCommand(settings, dependencies, stdout))
	root.AddCommand(mcpCommand(settings, dependencies, stdout, stderr))
	root.AddCommand(updateCommand(settings, dependencies, stdout, stderr))
	settings.jsonOutput = requestedJSON(arguments)
	command, err := root.ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	if errors.Is(err, auth.ErrNoLogin) {
		err = problem.New("login_required", "No login is saved. Run zorveus login.", 3)
	}
	failure := problem.Safe(err)
	if strings.HasPrefix(err.Error(), "unknown command ") {
		failure = problem.New("invalid_arguments", "The command could not be executed. Check zorveus --help for usage.", 2)
	}
	commandName := "zorveus"
	if command != nil {
		commandName = command.CommandPath()
	}
	if isMCPCommand(command) {
		// Even --json startup failures must leave the protocol stream untouched.
		logger = slog.New(slog.NewJSONHandler(stderr, nil))
		logger.Error("MCP server stopped", "code", failure.Code, "message", failure.Message)
		return failure.ExitCode
	}
	logger.Debug("command failed", "command", commandName, "code", failure.Code)
	if settings.jsonOutput {
		_ = json.NewEncoder(stdout).Encode(resultEnvelope{Version: 1, OK: false, Command: commandName, Error: failure})
	} else {
		message := planColor(terminalText(failure.Message), terminalError, terminalColors(settings, dependencies.ErrorIsTerminal))
		_, _ = fmt.Fprintln(stderr, message)
		if failure.Pointer != nil {
			_, _ = fmt.Fprintf(stderr, "JSON Pointer: %q\n", *failure.Pointer)
		}
	}
	return failure.ExitCode
}

func loginCommand(settings *flags, dependencies Dependencies, stdout, stderr io.Writer) *cobra.Command {
	var scope string
	var dashboardOrigin string
	var timeout time.Duration
	var manualBrowser bool
	command := &cobra.Command{
		Use:               "login",
		Short:             "Log in through browser consent and replace the saved login",
		Args:              noArguments,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(command *cobra.Command, arguments []string) error {
			ui := terminalUI{output: stdout, color: terminalColors(settings, dependencies.OutputIsTerminal)}
			if settings.nonInteractive || !dependencies.InputIsTerminal {
				return problem.New("interactive_login_required", "Login requires an interactive terminal and browser authorization.", 3)
			}
			if settings.apiOrigin == "" {
				settings.apiOrigin = "https://api.zorveus.com"
			}
			if dashboardOrigin == "" {
				dashboardOrigin = "https://app.zorveus.com"
				if settings.localMode {
					dashboardOrigin = "http://localhost:3000"
				}
			}
			dashboard, err := api.CanonicalOrigin(dashboardOrigin, settings.allowInsecureLocal)
			if err != nil {
				return err
			}
			scopes := strings.Join(strings.Fields(scope), " ")
			if scopes == "" || timeout <= 0 || timeout > 30*time.Minute {
				return problem.New("invalid_arguments", "Provide at least one scope and a login timeout greater than zero and at most 30 minutes.", 2)
			}
			result, err := browserLogin(command.Context(), settings, dependencies, stderr, auth.LoginOptions{
				APIOrigin:       settings.apiOrigin,
				DashboardOrigin: dashboard,
				Scope:           scopes,
			}, timeout, manualBrowser)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			if err := ui.success("Signed in"); err != nil {
				return err
			}
			if err := ui.field("Organization", result.Organization); err != nil {
				return err
			}
			return nil
		},
	}
	command.Flags().StringVar(&scope, "scope", defaultLoginScopes, "Space-separated management scopes; defaults to all CLI permissions")
	command.Flags().StringVar(&dashboardOrigin, "dashboard-url", "", "Override the dashboard origin; defaults to https://app.zorveus.com")
	command.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Maximum time to complete login")
	command.Flags().BoolVar(&manualBrowser, "no-browser", false, "Print the authorization URL so you can open it in your chosen browser")
	return command
}

func appsCommand(settings *flags, dependencies Dependencies, stdout io.Writer) *cobra.Command {
	var limit int
	apps := &cobra.Command{Use: "apps", Short: "Read apps in the current login's organization", ValidArgsFunction: cobra.NoFileCompletions}
	list := &cobra.Command{
		Use:               "list",
		Short:             "List apps; the API currently returns at most 1,000 per request",
		Args:              noArguments,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(command *cobra.Command, arguments []string) error {
			if limit < 1 || limit > 1000 {
				return problem.New("invalid_limit", "Use a limit between 1 and 1,000.", 2)
			}
			result, err := dependencies.Auth.Apps(command.Context(), settings.apiOrigin, limit)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(writer, "ID\tNAME\tSTATUS")
			for _, app := range result.Apps {
				_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\n", terminalText(app.ID), terminalText(app.Name), terminalText(app.Status))
			}
			if err := writer.Flush(); err != nil {
				return outputError(err)
			}
			if result.MayHaveMore {
				_, err := fmt.Fprintln(stdout, "The API returned the requested limit. Additional apps may exist; cursor pagination is not available yet.")
				return outputError(err)
			}
			return nil
		},
	}
	list.Flags().IntVar(&limit, "limit", 1000, "Maximum apps to return, between 1 and 1,000")
	apps.AddCommand(list)
	return apps
}

func logoutCommand(settings *flags, dependencies Dependencies, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:               "logout",
		Short:             "Revoke the current CLI session and remove its saved credentials",
		Args:              noArguments,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(command *cobra.Command, arguments []string) error {
			result, err := dependencies.Auth.Logout(command.Context(), settings.apiOrigin)
			if err != nil {
				return err
			}
			if settings.jsonOutput {
				return writeJSON(stdout, command.CommandPath(), result)
			}
			_, err = fmt.Fprintln(stdout, "Logged out.")
			return outputError(err)
		},
	}
}

func noArguments(command *cobra.Command, arguments []string) error {
	if len(arguments) != 0 {
		return problem.New("invalid_arguments", "This command does not accept positional arguments.", 2)
	}
	return nil
}

func writeJSON(writer io.Writer, command string, result any) error {
	err := json.NewEncoder(writer).Encode(resultEnvelope{Version: 1, OK: true, Command: command, Result: result})
	return outputError(err)
}

func outputError(err error) error {
	if err != nil {
		return problem.New("output_failed", "Could not write the command result.", 1)
	}
	return nil
}

func terminalText(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
}

func requestedJSON(arguments []string) bool {
	requested := false
	for _, argument := range arguments {
		switch argument {
		case "--":
			return requested
		case "--json", "--json=true":
			requested = true
		case "--json=false":
			requested = false
		}
	}
	return requested
}
