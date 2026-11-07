# Connect an MCP client to Zorveus

The CLI includes a local MCP server. A coding agent can inspect your account, prepare manifests, review plans, apply approved changes, check drift, and manage service keys through the same operations as the terminal commands.

## Sign in first

Install `zorveus`, then authorize it from a terminal:

```sh
zorveus login
zorveus whoami
```

Choose the organization that owns your app. For a local backend, use `zorveus login --local` instead.

The MCP server shares the CLI's saved login and refresh lock. It does not open a login browser. If a remote tool reports `login_required` or `authorization_required`, log in from a terminal and retry the tool. The server reads the current saved login for each operation, so a new login does not require a server restart.

Init, validation, clone, and promotion work offline without login. The server never returns management tokens or generated secrets to the agent.

## Configure your client

Configure a local stdio server with this executable and arguments:

```sh
zorveus mcp serve --directory /absolute/path/to/your/project
```

The client launches this command and communicates over its stdin and stdout. You do not need a running HTTP service or an MCP URL.

For clients that use an `mcpServers` configuration object:

```json
{
	"mcpServers": {
		"zorveus": {
			"command": "/absolute/path/to/zorveus",
			"args": [
				"mcp",
				"serve",
				"--directory",
				"/absolute/path/to/your/project"
			]
		}
	}
}
```

Replace both paths. Use the installed executable, or `bin/zorveus` from a local build. On Windows, use the absolute path to `zorveus.exe` and escape backslashes in JSON. Your client's configuration format may differ; the command and arguments stay the same.

`--directory` defaults to the process's working directory. Setting it explicitly avoids depending on the directory chosen by an editor or desktop client. Relative tool paths resolve against it; absolute paths also work. This option chooses a base directory, not a filesystem sandbox. Apply also uses the absolute manifest path recorded in its saved plan.

The server normally uses the saved login's API origin. Add `--local` to the server arguments to require the local API, or use `--api-url` with the existing CLI origin rules.

For BYOK, make the manifest's `secretFromEnv` variables available to the MCP process when your client launches it. An export in an unrelated terminal may not reach an editor or desktop client. The CLI does not load `.env` files automatically. Pass environment-variable names in the manifest, never secret values in tool arguments.

## Inspect your account and prepare a manifest

Ask the agent to call `zorveus_whoami` with `{}` to confirm the selected user and organization. `zorveus_apps_list` with `{}` lists up to 1,000 apps. Pass `{"limit":20}` for a smaller response. The result's `may_have_more` indicates that the requested limit was reached; the existing app API does not provide cursor pagination.

To prepare a new app, call `zorveus_init`:

```json
{
	"config": "zorveus.json",
	"name": "Assistant",
	"models": ["*"]
}
```

This creates a local file. It does not create the app until you plan and apply. With no arguments, init uses `zorveus.json`, the app name `My app`, and models `["*"]`.

To adopt an existing app instead, call `zorveus_import`:

```json
{
	"app_id": "app_existing",
	"config": "zorveus.json"
}
```

Import reads the existing app and writes a new manifest and managed state. It preserves backend IDs and does not change remote resources. It excludes secrets, customer connections, and shared organization credentials. The manifest and its CLI state must not already exist.

## Review and apply a change

Ask your agent to edit the manifest, validate it, and show the proposed changes before applying them. For example:

> Change the backend key's monthly cap to USD 200 in zorveus.json. Validate and plan it, then show me the changes before applying.

The agent can call the tools in this order:

1. `zorveus_validate` with `{"config":"zorveus.json"}`.
2. `zorveus_plan` with `{"config":"zorveus.json"}`.
3. Show you the plan's `changes` and ask for your approval.
4. After approval, call `zorveus_apply` with the returned plan path and `"yes":true`.
5. Call `zorveus_status` with `{"config":"zorveus.json"}`.

Plan leaves your input manifests and remote resources unchanged. It writes a saved plan, such as `zorveus.plan.json`. Apply creates or updates resources, saves returned IDs into the manifest, and delivers generated secrets to the OS credential store. The tool returns credential account names, not their values.

The apply tool requires `yes` to be explicitly supplied. Planned revocations, cap removals, and redirect removals also require `allow_delete:true`. These fields carry the user's approval; the MCP client is responsible for presenting the changes and obtaining that approval.

If either manifest or a planned remote value changes before apply starts, create a fresh plan. After an interrupted apply, retry the same saved plan to use its existing recovery journal.

## Plan from development to production

The plan tool supports the same cross-file settings as the CLI:

```json
{
	"config": "zorveus.prod.json",
	"from": "zorveus.dev.json",
	"keep_names": true
}
```

Call `zorveus_plan` with these arguments. Matching resources keep their production IDs. `keep_names:true` also preserves production display names. Omit it to copy supplied source names. The default saved plan is `zorveus.prod.plan.json`, and apply remembers the name choice. See the [cross-app planning guide](promotion.md) for merge and removal rules.

## Copy or update a local manifest

To prepare a separate app without deploying it, call `zorveus_clone`:

```json
{
	"from": "zorveus.json",
	"to": "zorveus.dev.json",
	"name": "Assistant dev"
}
```

Clone removes app, inference-key, and routing-key IDs while retaining settings and resource object names. Omit `name` to retain the source app name. The destination must be new. Clone rejects manifests containing service keys or provider credentials because they are shared organization resources.

To update an existing target file immediately, call `zorveus_promote`:

```json
{
	"from": "zorveus.dev.json",
	"to": "zorveus.prod.json",
	"keep_names": true
}
```

Promotion preserves matched target IDs and requires an existing target app ID. `keep_names:true` also keeps target display names. It changes the target JSON immediately, leaves the source unchanged, and does not change backend resources. Run plan and apply separately to deploy the result. Use the plan tool's `from` argument when you want to review the merge before updating the target file.

## Inspect and revoke service keys

Call `zorveus_service_keys_list` with `{}` to list metadata in the selected organization. Secret values are excluded.

After the user approves revoking a specific key, call `zorveus_service_keys_revoke`:

```json
{
	"id": "service_key_to_revoke",
	"yes": true
}
```

Revocation applies across the organization, including other apps using that key. The tool requires an explicit `yes` argument. A false value stops before accessing credentials or making a backend request. Retrying an already revoked key returns its metadata without another revocation request. This operation acts directly; it does not create a saved plan.

## Tool reference

All twelve tools return structured JSON and a text JSON fallback for clients that do not display structured content.

| Tool | Arguments | Result |
| --- | --- | --- |
| `zorveus_whoami` | None | Current user, session, selected organization, and issued scopes; no tokens |
| `zorveus_apps_list` | Optional `limit`, default 1,000, range 1 to 1,000 | App metadata, organization, result limit, and `may_have_more` |
| `zorveus_init` | Optional `config`, `name`, and `models` | New manifest path and version |
| `zorveus_import` | Required `app_id`; optional `config`, default `zorveus.json` | New manifest path, app ID, and imported key and router counts |
| `zorveus_clone` | Required `from` and `to`; optional `name` | New manifest path and version |
| `zorveus_promote` | Required `from` and `to`; optional `keep_names` | Source and target paths, and whether the target changed |
| `zorveus_validate` | Optional `config`, default `zorveus.json` | Manifest validity, version, and checks that require remote state |
| `zorveus_plan` | Optional `config`, `from`, `out`, and `keep_names` | Saved plan path, proposed changes, operation count, and required scopes |
| `zorveus_apply` | Required `plan` and `yes`; optional `allow_delete` | App ID, updated manifest path, completed steps, and credential account names |
| `zorveus_status` | Optional `config`, default `zorveus.json` | Resource drift, `in_sync`, and the last apply result |
| `zorveus_service_keys_list` | None | Organization and service-key metadata |
| `zorveus_service_keys_revoke` | Required `id` and `yes` | Revoked service-key metadata |

`keep_names` requires `from`. When `out` is omitted, the plan filename follows the target manifest, so `environments/prod.json` produces `environments/prod.plan.json`.

An operation failure sets the MCP result's `isError` and returns an `error` object containing a stable `code`, a readable `message`, and a JSON `pointer` when relevant. For example:

```json
{
	"error": {
		"code": "login_required",
		"message": "No login is saved. Run zorveus login in a terminal, then retry this tool."
	}
}
```

Login, logout, shell completion, and server startup remain terminal commands. MCP tools use the same saved login, role checks, and OAuth scope checks as their CLI equivalents.

## Diagnostics

Stdout contains only MCP protocol messages, including when `--json` is supplied. Startup failures and tool diagnostics go to stderr as JSON. Tool logs contain the tool name, duration, and safe error code; they exclude tokens, secrets, tool arguments, and raw manifests.

Use `zorveus mcp serve --help` for command options. Running the server directly in a terminal waits for an MCP client to send protocol messages. A client disconnect or an interrupt stops the server.

The adapter uses the [official Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk). Tool discovery and calls follow the [MCP tools specification](https://modelcontextprotocol.io/specification/latest/server/tools).
