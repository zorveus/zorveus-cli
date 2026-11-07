package cli

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/zorveus/zorveus-cli/internal/mcpserver"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func mcpCommand(settings *flags, dependencies Dependencies, stdout, stderr io.Writer) *cobra.Command {
	var directory string
	command := &cobra.Command{Use: "mcp", Short: "Connect coding agents to Zorveus", ValidArgsFunction: cobra.NoFileCompletions}
	serve := &cobra.Command{
		Use: "serve", Short: "Serve MCP tools over stdin and stdout", Args: noArguments,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(command *cobra.Command, arguments []string) error {
			var level slog.Level
			_ = level.UnmarshalText([]byte(settings.logLevel)) // Validated by the root command.
			logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: level}))
			server, err := mcpserver.New(mcpserver.Options{
				Auth: dependencies.Auth, Secrets: dependencies.Secrets, APIOrigin: settings.apiOrigin,
				Directory: directory, Logger: logger,
			})
			if err != nil {
				return err
			}
			input, ok := command.InOrStdin().(io.ReadCloser)
			if !ok {
				input = io.NopCloser(command.InOrStdin())
			}
			transport := &mcp.IOTransport{Reader: input, Writer: protocolWriter{stdout}}
			err = server.Run(command.Context(), transport)
			if errors.Is(err, context.Canceled) {
				return nil
			}
			if err != nil {
				return problem.New("mcp_transport_failed", "The MCP connection ended unexpectedly. Restart the server from your MCP client.", 1)
			}
			return nil
		},
	}
	serve.Flags().StringVar(&directory, "directory", ".", "Project directory used to resolve relative tool paths")
	_ = serve.MarkFlagDirname("directory")
	command.AddCommand(serve)
	return command
}

// The transport owns its connection, but stdout belongs to the caller.
type protocolWriter struct{ io.Writer }

func (protocolWriter) Close() error { return nil }

func isMCPCommand(command *cobra.Command) bool {
	for command != nil {
		if command.Name() == "mcp" && command.Parent() != nil && command.Parent().Name() == "zorveus" {
			return true
		}
		command = command.Parent()
	}
	return false
}
