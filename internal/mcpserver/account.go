package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func (tools *adapter) addAccountTools(server *mcp.Server) {
	remote := true
	mcp.AddTool(server, &mcp.Tool{
		Name: "zorveus_whoami", Description: "Verify the current user, organization, and session using the saved CLI login. Returns no tokens.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &remote},
	}, tools.whoami)
	mcp.AddTool(server, &mcp.Tool{
		Name: "zorveus_apps_list", Description: "List apps in the saved login's organization. Returns metadata only, with an optional result limit.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &remote},
	}, tools.appsList)
	mcp.AddTool(server, &mcp.Tool{
		Name: "zorveus_service_keys_list", Description: "List organization service-key metadata without returning secret values.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &remote},
	}, tools.serviceKeysList)
	mcp.AddTool(server, &mcp.Tool{
		Name: "zorveus_service_keys_revoke", Description: "Revoke a service key for the entire organization. It may be used by other apps. Set yes only after the user approves revoking this exact key.",
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: &remote},
	}, tools.serviceKeysRevoke)
}

type appsListInput struct {
	Limit *int `json:"limit,omitempty" jsonschema:"Maximum apps to return, from 1 to 1000; defaults to 1000"`
}

type serviceKeysRevokeInput struct {
	ID  string `json:"id" jsonschema:"Exact service-key ID in the selected organization"`
	Yes bool   `json:"yes" jsonschema:"Must be true only after the user approves organization-wide revocation of this key"`
}

func (tools *adapter) whoami(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	started := time.Now()
	result, err := tools.service.Auth.Whoami(ctx, tools.origin)
	return tools.reply(ctx, "zorveus_whoami", started, result, err)
}

func (tools *adapter) appsList(ctx context.Context, _ *mcp.CallToolRequest, input appsListInput) (*mcp.CallToolResult, any, error) {
	started := time.Now()
	limit := 1000
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 || limit > 1000 {
		return tools.reply(ctx, "zorveus_apps_list", started, nil, problem.New("invalid_limit", "Use a limit between 1 and 1,000.", 2))
	}
	result, err := tools.service.Auth.Apps(ctx, tools.origin, limit)
	return tools.reply(ctx, "zorveus_apps_list", started, result, err)
}

func (tools *adapter) serviceKeysList(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	started := time.Now()
	result, err := tools.service.ServiceKeys(ctx, tools.origin)
	return tools.reply(ctx, "zorveus_service_keys_list", started, result, err)
}

func (tools *adapter) serviceKeysRevoke(ctx context.Context, _ *mcp.CallToolRequest, input serviceKeysRevokeInput) (*mcp.CallToolResult, any, error) {
	started := time.Now()
	result, err := tools.service.RevokeServiceKey(ctx, tools.origin, input.ID, input.Yes)
	return tools.reply(ctx, "zorveus_service_keys_revoke", started, result, err)
}
