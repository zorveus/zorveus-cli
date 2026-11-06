package cli

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"time"
)

func OpenBrowser(ctx context.Context, address string) error {
	launchContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(launchContext, "open", address).Run()
	case "linux":
		return exec.CommandContext(launchContext, "xdg-open", address).Run()
	case "windows":
		return exec.CommandContext(launchContext, "rundll32", "url.dll,FileProtocolHandler", address).Run()
	default:
		return errors.New("browser opening is unavailable on this platform")
	}
}
