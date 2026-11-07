package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
	"github.com/zorveus/zorveus-cli/internal/provision"
)

func (tools *adapter) addManifestTools(server *mcp.Server) {
	falseValue, trueValue := false, true
	mcp.AddTool(server, &mcp.Tool{
		Name: "zorveus_init", Description: "Create a starter manifest offline. The destination must be new; this does not provision remote resources.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &falseValue, OpenWorldHint: &falseValue},
	}, tools.initManifest)
	mcp.AddTool(server, &mcp.Tool{
		Name: "zorveus_import", Description: "Read an existing app into a new local manifest and managed state, preserving its resource IDs. Does not change backend resources or return secrets. The destination must be new.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &falseValue, OpenWorldHint: &trueValue},
	}, tools.importManifest)
	mcp.AddTool(server, &mcp.Tool{
		Name: "zorveus_clone", Description: "Create a separate manifest offline, removing app-owned resource IDs. Leaves the source unchanged and refuses existing destinations or shared organization credentials.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &falseValue, OpenWorldHint: &falseValue},
	}, tools.cloneManifest)
	mcp.AddTool(server, &mcp.Tool{
		Name: "zorveus_promote", Description: "Update an existing target JSON immediately with source settings while keeping target IDs. Does not deploy. Use keep_names to preserve target display names; use plan with from to review changes before updating the file.",
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: &falseValue},
	}, tools.promoteManifest)
}

type initInput struct {
	Config string   `json:"config,omitempty" jsonschema:"New manifest path; defaults to zorveus.json; existing destinations are refused"`
	Name   *string  `json:"name,omitempty" jsonschema:"App display name; defaults to My app"`
	Models []string `json:"models,omitempty" jsonschema:"Allowed model IDs; defaults to [\"*\"]; an explicitly empty list is invalid"`
}

type importInput struct {
	AppID  string `json:"app_id" jsonschema:"Existing app ID in the selected organization"`
	Config string `json:"config,omitempty" jsonschema:"New manifest path; defaults to zorveus.json; existing manifest or state is refused"`
}

type cloneInput struct {
	From string  `json:"from" jsonschema:"Source manifest path"`
	To   string  `json:"to" jsonschema:"New destination manifest path; existing destinations are refused"`
	Name *string `json:"name,omitempty" jsonschema:"Optional replacement app display name; must not be empty"`
}

type promoteInput struct {
	From      string `json:"from" jsonschema:"Source manifest supplying the desired settings"`
	To        string `json:"to" jsonschema:"Existing target manifest to update immediately, preserving its resource IDs"`
	KeepNames bool   `json:"keep_names,omitempty" jsonschema:"Preserve target display names instead of copying source names"`
}

func (tools *adapter) initManifest(ctx context.Context, _ *mcp.CallToolRequest, input initInput) (*mcp.CallToolResult, any, error) {
	started := time.Now()
	name := "My app"
	if input.Name != nil {
		name = *input.Name
	}
	models := input.Models
	// Omission uses the CLI default; an explicit empty list still fails validation.
	if models == nil {
		models = []string{"*"}
	}
	result, err := manifest.Init(tools.path(input.Config, "zorveus.json"), name, models)
	return tools.reply(ctx, "zorveus_init", started, result, err)
}

func (tools *adapter) importManifest(ctx context.Context, _ *mcp.CallToolRequest, input importInput) (*mcp.CallToolResult, any, error) {
	started := time.Now()
	result, err := tools.service.Import(ctx, tools.origin, input.AppID, tools.path(input.Config, "zorveus.json"))
	return tools.reply(ctx, "zorveus_import", started, result, err)
}

func (tools *adapter) cloneManifest(ctx context.Context, _ *mcp.CallToolRequest, input cloneInput) (*mcp.CallToolResult, any, error) {
	started := time.Now()
	if input.From == "" || input.To == "" {
		return tools.reply(ctx, "zorveus_clone", started, nil, problem.New("invalid_arguments", "Provide both from and to for clone.", 2))
	}
	name := ""
	if input.Name != nil {
		name = *input.Name
		if name == "" {
			return tools.reply(ctx, "zorveus_clone", started, nil, problem.New("invalid_arguments", "The replacement app name must not be empty.", 2))
		}
	}
	result, err := manifest.Clone(tools.path(input.From, ""), tools.path(input.To, ""), name)
	return tools.reply(ctx, "zorveus_clone", started, result, err)
}

func (tools *adapter) promoteManifest(ctx context.Context, _ *mcp.CallToolRequest, input promoteInput) (*mcp.CallToolResult, any, error) {
	started := time.Now()
	if input.From == "" || input.To == "" {
		return tools.reply(ctx, "zorveus_promote", started, nil, problem.New("invalid_arguments", "Provide both from and to for promote.", 2))
	}
	result, err := provision.Promote(ctx, tools.path(input.From, ""), tools.path(input.To, ""), input.KeepNames)
	return tools.reply(ctx, "zorveus_promote", started, result, err)
}
