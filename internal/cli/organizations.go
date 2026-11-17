package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func promptDefaultOrganization(ctx context.Context, settings *flags, dependencies Dependencies, reader io.Reader, output io.Writer, login auth.LoginSummary) error {
	input := &setupInput{ctx: ctx, reader: bufio.NewReader(reader), output: output, color: terminalColors(settings, dependencies.ErrorIsTerminal)}
	if err := input.ui().success("Signed in to " + terminalText(auth.OrganizationLabel(login.OrganizationName, login.Organization))); err != nil {
		return err
	}
	approved, err := input.confirm("Use this organization as your default?", true)
	if err != nil && problem.Safe(err).Code == "setup_canceled" {
		return problem.New("default_selection_canceled", "Login saved. Default organization was not changed.", 130)
	}
	if err != nil || !approved {
		return err
	}
	return dependencies.Auth.SetDefault(ctx, auth.LoginReference{APIOrigin: login.APIOrigin, Organization: login.Organization})
}

func organizationPicker(settings *flags, dependencies Dependencies, reader io.Reader, output io.Writer) func(context.Context, []auth.LoginReference) (auth.LoginReference, error) {
	return func(ctx context.Context, choices []auth.LoginReference) (auth.LoginReference, error) {
		input := &setupInput{ctx: ctx, reader: bufio.NewReader(reader), output: output, color: terminalColors(settings, dependencies.ErrorIsTerminal)}
		for index, choice := range choices {
			if _, err := fmt.Fprintf(output, "  %d. %s  %s\n", index+1, terminalText(choice.Organization), terminalText(choice.APIOrigin)); err != nil {
				return auth.LoginReference{}, outputError(err)
			}
		}
		for {
			answer, err := input.ask("Organization number", "")
			if err != nil {
				return auth.LoginReference{}, err
			}
			index, err := strconv.Atoi(answer)
			if err == nil && index > 0 && index <= len(choices) {
				return choices[index-1], nil
			}
			input.ui().warning("Enter a number from the list.")
		}
	}
}
