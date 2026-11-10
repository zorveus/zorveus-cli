package cli

import (
	"fmt"
	"io"
	"os"
)

// Use the terminal's palette so the same hierarchy works in light and dark themes.
const (
	terminalAccent  = "36"
	terminalSuccess = "32"
	terminalWarning = "33"
	terminalError   = "31"
	terminalMuted   = "2"
	terminalBold    = "1"
)

type terminalUI struct {
	output io.Writer
	color  bool
}

func terminalColors(settings *flags, isTerminal bool) bool {
	return isTerminal && !settings.noColor && !settings.jsonOutput && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

func (ui terminalUI) text(value, style string) string {
	return planColor(terminalText(value), style, ui.color)
}

func (ui terminalUI) heading(title string) error {
	_, err := fmt.Fprintf(ui.output, "\n%s\n", ui.text(title, terminalBold))
	return outputError(err)
}

func (ui terminalUI) note(message string) error {
	_, err := fmt.Fprintf(ui.output, "  %s\n", ui.text(message, terminalMuted))
	return outputError(err)
}

func (ui terminalUI) field(label, value string) error {
	_, err := fmt.Fprintf(ui.output, "  %s %s\n", ui.text(fmt.Sprintf("%-12s", label), terminalMuted), terminalText(value))
	return outputError(err)
}

func (ui terminalUI) success(message string) error {
	_, err := fmt.Fprintf(ui.output, "  %s %s\n", ui.text("✓", terminalSuccess), terminalText(message))
	return outputError(err)
}

func (ui terminalUI) warning(message string) error {
	_, err := fmt.Fprintf(ui.output, "  %s %s\n", ui.text("!", terminalWarning), ui.text(message, terminalWarning))
	return outputError(err)
}
