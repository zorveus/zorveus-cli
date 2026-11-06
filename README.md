# Zorveus CLI

Manage your Zorveus apps, inference keys, routing keys, spending limits, and provider credentials from the terminal.

Describe an app in `zorveus.json`, review the proposed changes, and apply them to your organization. You can also import an existing app, check for configuration drift, or prepare a separate development copy.

## Build from source

With Go 1.26 or newer installed, run these commands from this repository:

```sh
go build -o bin/zorveus ./cmd/zorveus
./bin/zorveus --help
```

The built executable does not need Go installed to run. Login and credential creation require an available OS credential store: Keychain on macOS, Secret Service on Linux, or Credential Manager on Windows.

## Sign in

```sh
./bin/zorveus login
./bin/zorveus whoami
./bin/zorveus apps list
```

Login opens the Zorveus dashboard, where you choose an organization and approve access. It uses `https://api.zorveus.com` and `https://app.zorveus.com` by default.

The default permission is `apps:read`. To create the app, keys, cap, and router in the example below, request these permissions instead:

```sh
./bin/zorveus login --scope "apps:read apps:create apps:rotate_secret app_connections:read app_connections:write caps:app_write routing_keys:read routing_keys:write"
```

The CLI keeps one saved login. Signing in again replaces it. To request different permissions or switch organizations, run `login` again. Your organization role still determines which operations you can perform.

Use `login --no-browser` to print the authorization URL and open it in your preferred browser. To end the CLI session:

```sh
./bin/zorveus logout
```

## Configure an app

Create a starter manifest:

```sh
./bin/zorveus init --name "Assistant"
```

The default file is `zorveus.json`. New apps allow all models with `allowedModels: ["*"]`. To select specific models, repeat `--model` when running `init`.

Edit the manifest to describe the resources you need. This example creates an app, an inference key with a monthly USD cap, and a routing key that selects it:

```json
{
  "version": 1,
  "app": {
    "name": "Assistant",
    "allowedModels": ["*"]
  },
  "keys": [
    {
      "name": "Backend",
      "routingAlias": "backend",
      "cap": {
        "amount": "100",
        "currency": "USD",
        "period": "monthly"
      }
    }
  ],
  "routingKeys": [
    {
      "name": "Assistant router",
      "members": ["backend"],
      "defaultSelector": "backend"
    }
  ]
}
```

`routingAlias` connects manifest entries. A routing key's members refer to these aliases. Each inference key also works directly, without a router. Write monetary amounts as strings to preserve decimal precision.

Validate the file:

```sh
./bin/zorveus validate
```

`init`, `validate`, and `clone` work offline without login. Validation checks the file's structure and references; planning checks remote resources and access.

See the [manifest reference](docs/manifest-v1.md) for OAuth settings, consent defaults, pricing defaults, key policies, and the full update rules.

## Review and apply changes

```sh
./bin/zorveus plan --config zorveus.json --out zorveus.plan.json
```

Plan shows the proposed changes and saves them without changing remote resources. Review its output, then apply that saved plan:

```sh
./bin/zorveus apply --plan zorveus.plan.json --yes
./bin/zorveus status --config zorveus.json
```

Apply creates or updates the resources and writes their IDs back into your manifest. Keep those IDs when editing existing resources. Running plan again after a successful apply produces zero changes when the configuration matches.

The CLI saves newly generated secrets in your OS credential store and reports their account names. Secrets do not appear in the manifest, saved plans, or command output. On macOS, use Keychain Access to find entries under `com.zorveus.cli.resources` with the reported account name.

If you edit the manifest or a planned remote value changes, create a new plan before applying. Use a new `--out` filename, since plan refuses to overwrite an existing file.

### Remove managed resources

Omitted fields and resource sections preserve existing values. Removing a previously managed key or router from an explicit array proposes its revocation. An empty array removes all resources recorded for that section of the manifest. Unrelated resources are left alone.

Planned revocations, cap removal, and redirect removal need an additional flag:

```sh
./bin/zorveus apply --plan removal.plan.json --yes --allow-delete
```

Service keys and provider credentials belong to the organization. Removing their entries from a manifest leaves those shared resources intact.

### Resume an interrupted apply

Keep the saved plan and the `.zorveus/` directory beside your manifest, then repeat the same apply command:

```sh
./bin/zorveus apply --plan zorveus.plan.json --yes
```

The CLI uses its recorded progress to resume. If a resource ID is known but its generated secret was not saved, recovery can rotate the credential. If a creation or rotation outcome is unknown, apply stops to avoid repeating an uncertain operation. Inspect the resource and reported journal before continuing. An unfinished apply blocks a new plan for the same manifest.

## Import an existing app

```sh
./bin/zorveus login --scope "apps:read app_connections:read routing_keys:read"
./bin/zorveus import --app-id app_example --config imported.json
./bin/zorveus status --config imported.json
```

Import writes the app's settings, active programmatic inference connections, and routing keys to a new manifest, preserving their IDs. It does not import secrets, customer connections, or shared organization credentials. The destination must not already exist.

Status reports changed fields, missing or revoked resources, and the latest apply result without changing anything. In JSON output, check `in_sync` to determine whether the remote configuration matches.

## Create a development copy

```sh
./bin/zorveus clone --from zorveus.json --to zorveus.dev.json --name "Assistant development"
./bin/zorveus validate --config zorveus.dev.json
./bin/zorveus plan --config zorveus.dev.json --out development.plan.json
./bin/zorveus apply --plan development.plan.json --yes
```

Clone preserves configuration and aliases while removing app, inference-key, and routing-key IDs. Applying the copy creates separate resources. The source is unchanged, and the destination must be new.

Clone rejects manifests containing service keys or provider credentials because those resources are shared across the organization. Prepare a manifest without those sections before cloning.

## Bring your own provider key

Add a `providerCredentials` section to your app manifest:

```json
{
  "providerCredentials": [
    {
      "name": "OpenAI primary",
      "provider": "openai",
      "secretFromEnv": "OPENAI_API_KEY",
      "routingMode": "auto_resolve",
      "defaultModelPolicy": ["openai/gpt-4o-mini"]
    }
  ]
}
```

Set `OPENAI_API_KEY` in the environment of the shell running the CLI. The CLI does not automatically load `.env` files. Keep the actual key out of the manifest.

Request `provider_credentials:read` and `provider_credentials:write` alongside the permissions for the app and keys you manage, then run plan and apply. The model must also be allowed by your app and inference key. With `auto_resolve`, eligible connections in the organization can use the credential according to its model policy.

Provider metadata updates preserve the stored secret. A changed secret input proposes rotation; an unchanged input and active version produce no rotation. Status can compare the remembered input when its environment variable is available. An unavailable variable is reported as `unverified`.

Use `providerConfig` for nonsecret provider settings. Azure requires `api_base` and `api_version`; Vertex requires `project` and `location`. See the [shared credential rules](docs/manifest-v1.md#shared-organization-resources) for supported settings and secret formats.

## Manage service keys

Declare `serviceKeys` in a manifest to create or manage organization service keys. Each entry contains a name and machine scopes, or an existing ID to reuse. Newly generated secrets go to the OS credential store.

Request `service_keys:read`, `service_keys:write`, and, for revocation, `service_keys:revoke`. Service-key management requires organization-wide consent.

```sh
./bin/zorveus service-keys list
./bin/zorveus service-keys revoke --id service_example --yes
```

Revocation affects the key across the organization. Service-key payload scopes describe what the generated key can do; they are separate from the permissions granted to your CLI login.

## Local development

With the management API on port 8000 and dashboard on port 3000:

```sh
./bin/zorveus login --local
./bin/zorveus whoami
./bin/zorveus apps list
```

`--local` selects `http://localhost:8000` and `http://localhost:3000`. Subsequent commands use the saved login's API origin. Add `--scope` to the local login command when you need write permissions.

For other environments, use `--api-url` and `login --dashboard-url`. Custom HTTP origins must use loopback addresses and require `--allow-insecure-loopback`.

## Shell completion

Load zsh completion in your current terminal:

```zsh
autoload -Uz compinit
compinit
source <(./bin/zorveus completion zsh)
```

For future terminals, add the `source` line to `~/.zshrc` with the executable's absolute path. If your shell already runs `compinit`, keep its existing setup. Bash, fish, and PowerShell are also supported:

```sh
./bin/zorveus completion --help
```

## Scripts and JSON output

Use `--json` for structured results and `--non-interactive` to prevent interactive login:

```sh
./bin/zorveus status --config zorveus.json --json --non-interactive
```

JSON results include `version`, `ok`, `command`, and either `result` or `error`. Diagnostics go to stderr. Status exits zero after a successful read even when configuration differs, so check `result.in_sync` in scripts.

| Exit code | Meaning |
| --- | --- |
| 0 | Command succeeded |
| 1 | Operational failure or incomplete logout/login replacement |
| 2 | Invalid arguments or manifest |
| 3 | Login or additional permissions required |
| 4 | Target mismatch, stale plan, or changed manifest |
| 5 | Incomplete apply or uncertain mutation outcome |

Login requires an interactive terminal and browser consent. Other commands can use a saved login with an available OS credential store. Renewal is optional during consent; without it, sign in again after the access token expires. Hosts without an OS credential store are not supported.

## Command reference

| Command | Purpose |
| --- | --- |
| `login`, `whoami`, `logout` | Manage your CLI session |
| `apps list` | List apps in the selected organization |
| `init`, `validate` | Create and check a manifest offline |
| `plan`, `apply` | Review and apply configuration changes |
| `import` | Adopt an existing app into a manifest |
| `clone` | Prepare a separate app configuration |
| `status` | Compare the manifest with remote resources |
| `service-keys list`, `service-keys revoke` | Inspect and revoke organization service keys |
| `completion` | Generate shell completion scripts |

Run `./bin/zorveus <command> --help` for flags. See the [manifest reference](docs/manifest-v1.md) and [JSON Schema](internal/manifest/schema/v1.json) for configuration details.

## Contribute

Run tests and static checks:

```sh
go test -race ./...
go vet ./...
```

Build executables for macOS, Linux, and Windows on amd64 and arm64:

```sh
sh scripts/check-release-builds.sh
```
