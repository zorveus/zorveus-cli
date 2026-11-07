package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zorveus/zorveus-cli/internal/auth"
	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

type Options struct {
	Auth      *auth.Service
	Secrets   provision.SecretStore
	APIOrigin string
	Directory string
	Logger    *slog.Logger
}

type adapter struct {
	service   provision.Service
	origin    string
	directory string
	logger    *slog.Logger
}

// New registers tools over the same services used by terminal commands.
func New(options Options) (*mcp.Server, error) {
	directory := options.Directory
	if directory == "" {
		directory = "."
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, problem.New("invalid_directory", "Provide an existing project directory with --directory.", 2)
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil, problem.New("invalid_directory", "Provide an existing project directory with --directory.", 2)
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	tools := &adapter{
		service: provision.Service{Auth: options.Auth, Secrets: options.Secrets},
		origin:  options.APIOrigin, directory: directory, logger: logger,
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "zorveus", Version: "1"}, &mcp.ServerOptions{
		Instructions: "Manage Zorveus through local manifests. Run zorveus login in a terminal before remote operations. " +
			"Use zorveus_whoami to verify the selected organization before making changes. " +
			"Validate, plan, show the proposed changes to the user, then apply that saved plan only with the user's approval. " +
			"Service-key revocation also requires the user's approval for the exact key. " +
			"Relative paths use the server's project directory. Secrets remain in the OS credential store or environment.",
		Capabilities: &mcp.ServerCapabilities{},
	})
	// Keep SDK diagnostics disabled: protocol errors may contain client input.
	// Our own logger records tool names, durations, and safe error codes only.
	falseValue, trueValue := false, true
	mcp.AddTool(server, &mcp.Tool{
		Name: "zorveus_validate", Description: "Validate a local manifest and its references without login or backend requests.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &falseValue},
	}, tools.validate)
	mcp.AddTool(server, &mcp.Tool{
		Name: "zorveus_plan", Description: "Read remote resources and save proposed changes. Does not change remote resources or input manifests. Show the returned changes to the user before apply.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &falseValue, OpenWorldHint: &trueValue},
	}, tools.plan)
	mcp.AddTool(server, &mcp.Tool{
		Name: "zorveus_apply", Description: "Apply or resume a saved plan after the user approves its changes. Writes remote resources and local IDs. Never set yes or allow_delete without the user's approval.",
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: &trueValue},
	}, tools.apply)
	mcp.AddTool(server, &mcp.Tool{
		Name: "zorveus_status", Description: "Read remote resource state, configuration drift, and the last apply result without changing resources or manifests.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &trueValue},
	}, tools.status)
	tools.addAccountTools(server)
	tools.addManifestTools(server)
	return server, nil
}

type configInput struct {
	Config string `json:"config,omitempty" jsonschema:"Manifest path; defaults to zorveus.json in the project directory"`
}

type planInput struct {
	Config    string `json:"config,omitempty" jsonschema:"Target manifest path; defaults to zorveus.json"`
	From      string `json:"from,omitempty" jsonschema:"Optional source manifest; matching target IDs are preserved"`
	Out       string `json:"out,omitempty" jsonschema:"Saved plan path; defaults to the target filename with .plan.json"`
	KeepNames bool   `json:"keep_names,omitempty" jsonschema:"With from, preserve target display names instead of copying source names"`
}

type applyInput struct {
	Plan        string `json:"plan" jsonschema:"Exact saved plan path returned by zorveus_plan"`
	Yes         bool   `json:"yes" jsonschema:"Must be true only after the user approves the saved plan"`
	AllowDelete bool   `json:"allow_delete,omitempty" jsonschema:"Also permit planned revocations and removals after explicit user approval"`
}

func (tools *adapter) path(value, fallback string) string {
	if value == "" {
		value = fallback
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(tools.directory, value)
}

func (tools *adapter) validate(ctx context.Context, _ *mcp.CallToolRequest, input configInput) (*mcp.CallToolResult, any, error) {
	started := time.Now()
	config := tools.path(input.Config, "zorveus.json")
	document, err := manifest.Load(config)
	if err != nil {
		return tools.reply(ctx, "zorveus_validate", started, nil, err)
	}
	return tools.reply(ctx, "zorveus_validate", started, document.Result(config), nil)
}

func (tools *adapter) plan(ctx context.Context, _ *mcp.CallToolRequest, input planInput) (*mcp.CallToolResult, any, error) {
	started := time.Now()
	config := tools.path(input.Config, "zorveus.json")
	output := strings.TrimSuffix(config, filepath.Ext(config)) + ".plan.json"
	if input.Out != "" {
		output = tools.path(input.Out, "")
	}
	source := ""
	if input.From != "" {
		source = tools.path(input.From, "")
	}
	result, err := tools.service.PlanFrom(ctx, tools.origin, config, output, source, input.KeepNames)
	return tools.reply(ctx, "zorveus_plan", started, result, err)
}

func (tools *adapter) apply(ctx context.Context, _ *mcp.CallToolRequest, input applyInput) (*mcp.CallToolResult, any, error) {
	started := time.Now()
	if input.Plan == "" {
		return tools.reply(ctx, "zorveus_apply", started, nil, problem.New("invalid_arguments", "Provide the saved plan path returned by zorveus_plan.", 2))
	}
	result, err := tools.service.Apply(ctx, tools.origin, tools.path(input.Plan, ""), input.Yes, input.AllowDelete)
	return tools.reply(ctx, "zorveus_apply", started, result, err)
}

func (tools *adapter) status(ctx context.Context, _ *mcp.CallToolRequest, input configInput) (*mcp.CallToolResult, any, error) {
	started := time.Now()
	result, err := tools.service.Status(ctx, tools.origin, tools.path(input.Config, "zorveus.json"))
	return tools.reply(ctx, "zorveus_status", started, result, err)
}

func (tools *adapter) reply(ctx context.Context, name string, started time.Time, value any, err error) (*mcp.CallToolResult, any, error) {
	if err == nil {
		tools.logger.InfoContext(ctx, "tool completed", "tool", name, "duration_ms", time.Since(started).Milliseconds())
		return nil, value, nil
	}
	if errors.Is(err, auth.ErrNoLogin) {
		err = problem.New("login_required", "No login is saved. Run zorveus login in a terminal, then retry this tool.", 3)
	}
	failure := problem.Safe(err)
	tools.logger.WarnContext(ctx, "tool failed", "tool", name, "code", failure.Code, "duration_ms", time.Since(started).Milliseconds())
	// Return only classified errors, never raw SDK, credential-store, or HTTP errors.
	content := struct {
		Error *problem.Error `json:"error"`
	}{Error: failure}
	data, _ := json.Marshal(content)
	return &mcp.CallToolResult{
		IsError: true, StructuredContent: content,
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil, nil
}
