package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPStartupErrorsLeaveStdoutEmptyEvenWithJSON(t *testing.T) {
	for _, arguments := range [][]string{
		{"mcp", "serve", "--directory", filepath.Join(t.TempDir(), "missing"), "--json"},
		{"mcp", "serve", "--log-level", "invalid", "--json"},
		{"mcp", "serve", "--unknown-flag", "--json"},
		{"mcp", "serve", "unexpected", "--json"},
	} {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), arguments, &stdout, &stderr, Dependencies{})
		var diagnostic map[string]any
		if code != 2 || stdout.Len() != 0 || json.Unmarshal(stderr.Bytes(), &diagnostic) != nil || diagnostic["code"] == nil {
			t.Fatalf("MCP startup wrote non-protocol output: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	}
}

func TestMCPStdioCompletesHandshakeAndServesTools(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "zorveus.json"), []byte(`{"version":1,"app":{"name":"Assistant","allowedModels":["*"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	defer serverInput.Close()
	defer clientOutput.Close()
	defer clientInput.Close()
	defer serverOutput.Close()
	var diagnostics bytes.Buffer
	finished := make(chan int, 1)
	go func() {
		finished <- Execute(ctx, []string{"mcp", "serve", "--directory", directory, "--json"}, serverOutput, &diagnostics, Dependencies{Stdin: serverInput})
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: clientInput, Writer: clientOutput}, nil)
	if err != nil {
		t.Fatal(err)
	}
	listing, err := session.ListTools(ctx, nil)
	if err != nil || len(listing.Tools) != 12 {
		t.Fatalf("stdio tools/list failed: %+v %v", listing, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "zorveus_validate", Arguments: map[string]any{}})
	if err != nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("stdio tools/call failed: %+v %v", result, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("stdio server failed after disconnect: %s", diagnostics.String())
		}
	case <-ctx.Done():
		t.Fatal("stdio server did not stop when its client disconnected")
	}
	for _, line := range strings.Split(strings.TrimSpace(diagnostics.String()), "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil || event["msg"] != "tool completed" {
			t.Fatalf("unexpected stderr diagnostics: %s", diagnostics.String())
		}
	}
}

func TestMCPStdioStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	finished := make(chan int, 1)
	go func() {
		finished <- Execute(ctx, []string{"mcp", "serve"}, io.Discard, io.Discard, Dependencies{Stdin: input})
	}()
	cancel()
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatal("cancellation was not a clean shutdown")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MCP server ignored cancellation")
	}
}
