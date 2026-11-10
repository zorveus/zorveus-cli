package main

import (
	"context"
	"os"
	"os/signal"
	"time"

	"golang.org/x/term"

	"github.com/zorveus/zorveus-cli/internal/api"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/cli"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	service := &auth.Service{
		API:   api.NewClient(),
		Store: auth.CredentialStore{},
		Lock:  auth.LockCredentials,
		Now:   time.Now,
	}
	return cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr, cli.Dependencies{
		Auth:             service,
		Stdin:            os.Stdin,
		InputIsTerminal:  term.IsTerminal(int(os.Stdin.Fd())),
		OutputIsTerminal: term.IsTerminal(int(os.Stdout.Fd())),
		ErrorIsTerminal:  term.IsTerminal(int(os.Stderr.Fd())),
		OpenBrowser:      cli.OpenBrowser,
	})
}
