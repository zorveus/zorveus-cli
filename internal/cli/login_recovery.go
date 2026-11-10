package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func loginRecovery(settings *flags, dependencies Dependencies, input io.Reader, stderr io.Writer) func(context.Context, auth.LoginOptions) error {
	return func(ctx context.Context, options auth.LoginOptions) error {
		// Older saved logins did not include their dashboard origin.
		if options.DashboardOrigin == "" {
			switch options.APIOrigin {
			case "https://api.zorveus.com":
				options.DashboardOrigin = "https://app.zorveus.com"
			case "http://localhost:8000", "http://127.0.0.1:8000", "http://[::1]:8000":
				options.DashboardOrigin = "http://localhost:3000"
			default:
				return problem.New("login_required", "This saved login has no dashboard URL. Run zorveus login with --api-url and --dashboard-url once to enable sign-in recovery.", 3)
			}
		}
		dashboard, err := api.CanonicalOrigin(options.DashboardOrigin, true)
		if err != nil {
			return err
		}
		options.DashboardOrigin = dashboard
		ui := terminalUI{output: stderr, color: terminalColors(settings, dependencies.ErrorIsTerminal)}
		if err := ui.warning("Your session could not be renewed. Sign in again to continue."); err != nil {
			return err
		}
		if err := waitForLoginApproval(ctx, input, ui); err != nil {
			return err
		}
		_, err = browserLogin(ctx, settings, dependencies, stderr, options, 5*time.Minute, false)
		return err
	}
}

func waitForLoginApproval(ctx context.Context, input io.Reader, ui terminalUI) error {
	for {
		if ctx.Err() != nil {
			return loginApprovalCanceled()
		}
		if _, err := fmt.Fprintf(ui.output, "  %s ", ui.text("Press Enter to open your browser, or Ctrl+C to cancel.", terminalAccent)); err != nil {
			return outputError(err)
		}
		type answer struct {
			blank bool
			err   error
		}
		read := make(chan answer, 1)
		go func() {
			blank, err := readLoginApprovalLine(input)
			read <- answer{blank: blank, err: err}
		}()
		select {
		case <-ctx.Done():
			return loginApprovalCanceled()
		case result := <-read:
			if result.err != nil || ctx.Err() != nil {
				return loginApprovalCanceled()
			}
			if result.blank {
				return nil
			}
		}
	}
}

func readLoginApprovalLine(input io.Reader) (bool, error) {
	blank := true
	var character [1]byte
	// Leave subsequent input for the wizard or the next terminal prompt.
	for {
		count, err := input.Read(character[:])
		if count > 0 {
			switch character[0] {
			case '\n':
				return blank, nil
			case ' ', '\t', '\r':
			default:
				blank = false
			}
		}
		if err != nil {
			return false, err
		}
	}
}

func loginApprovalCanceled() error {
	return problem.New("login_canceled", "Sign-in canceled. The command stopped without sending the pending API request.", 3)
}

func browserLogin(ctx context.Context, settings *flags, dependencies Dependencies, stderr io.Writer, options auth.LoginOptions, timeout time.Duration, manual bool) (auth.LoginSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ui := terminalUI{output: stderr, color: terminalColors(settings, dependencies.ErrorIsTerminal)}
	options.OpenBrowser = func(ctx context.Context, address string) {
		if manual {
			_, _ = fmt.Fprintln(stderr, "Open in browser:", address)
			return
		}
		ui.note("Waiting for browser sign-in...")
		if dependencies.OpenBrowser == nil || dependencies.OpenBrowser(ctx, address) != nil {
			_, _ = fmt.Fprintln(stderr, "Open in browser:", address)
		}
	}
	result, err := dependencies.Auth.Login(ctx, options)
	if err != nil {
		return result, err
	}
	if result.PreviousSessionMayRemain {
		ui.warning("Previous session may be active. Revoke it in the dashboard.")
	}
	if !result.Renewal {
		ui.warning("Session won't renew. Sign in again after it expires.")
	}
	return result, nil
}
