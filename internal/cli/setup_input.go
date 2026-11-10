package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

type setupInput struct {
	ctx    context.Context
	reader *bufio.Reader
	output io.Writer
	color  bool
}

func (input *setupInput) ask(label, fallback string) (string, error) {
	return input.question(label, fallback, fallback)
}

func (input *setupInput) ui() terminalUI {
	return terminalUI{output: input.output, color: input.color}
}

func (input *setupInput) question(label, fallback, hint string) (string, error) {
	if input.ctx.Err() != nil {
		return "", setupCanceled()
	}
	ui := input.ui()
	prompt := "  " + ui.text("?", terminalAccent) + " " + ui.text(label, terminalBold)
	if hint != "" {
		prompt += " " + ui.text("("+hint+")", terminalMuted)
	}
	prompt += " " + ui.text("›", terminalAccent) + " "
	if _, err := fmt.Fprint(input.output, prompt); err != nil {
		return "", outputError(err)
	}
	type answer struct {
		value string
		err   error
	}
	read := make(chan answer, 1)
	// A canceled browser/login or terminal session must also stop a pending prompt.
	go func() {
		value, err := input.reader.ReadString('\n')
		read <- answer{value, err}
	}()
	select {
	case <-input.ctx.Done():
		return "", setupCanceled()
	case result := <-read:
		if result.err != nil || input.ctx.Err() != nil {
			return "", setupCanceled()
		}
		value := strings.TrimSpace(result.value)
		if value == "" {
			return fallback, nil
		}
		return value, nil
	}
}

func (input *setupInput) confirm(label string, defaultYes bool) (bool, error) {
	choices, fallback := "y/N", "no"
	if defaultYes {
		choices, fallback = "Y/n", "yes"
	}
	for {
		value, err := input.question(label, fallback, choices)
		if err != nil {
			return false, err
		}
		if value == "" {
			value = fallback
		}
		switch strings.ToLower(value) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		input.ui().warning("Enter y or n.")
	}
}

func setupCanceled() error {
	return problem.New("setup_canceled", "Setup canceled. No further changes were applied.", 130)
}
