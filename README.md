# Zorveus CLI

Manage your Zorveus apps, inference keys, routing keys, spending limits, and provider credentials from the terminal.

Describe an app in `zorveus.json`, review the proposed changes, and apply them to your organization. You can also import an existing app, check for configuration drift, or prepare a separate development copy.

To use these workflows from a coding agent, connect the CLI's [local MCP server](docs/mcp.md).

## Install

You do not need Go or a source checkout to run the CLI.

The hosted commands below require a published CLI release and the website download redirects. See the [release guide](docs/releasing.md) for deployment setup.

### macOS and Linux

```sh
curl -fsSL https://zorveus.com/install.sh | sh
```

The installer detects macOS or Linux and selects the x86-64 or ARM64 binary. It verifies the download's SHA-256 checksum and installs `zorveus` into `~/.local/bin` without `sudo`. Linux binaries are built without cgo, so the installer does not depend on a distribution's package manager or choice of glibc or musl.

You need `curl`, CA certificates, and either `sha256sum` or `shasum`. Other operating systems and processor architectures are not supported by the shell installer.

If `zorveus` is not found after installation, add its directory to your current shell:

```sh
export PATH="$HOME/.local/bin:$PATH"
zorveus --help
```

Add that `export PATH` line to your shell's startup file, such as `~/.zshrc` for zsh or `~/.bashrc` for Bash, to keep it available in new terminals.

Run the installer again to update to the latest release. To inspect the script, choose a version, or change the destination, download it first:

```sh
curl -fsSL https://zorveus.com/install.sh -o install-zorveus.sh
sh install-zorveus.sh --help
sh install-zorveus.sh --version v1.2.3 --bin-dir "$HOME/.local/bin"
```

Replace `v1.2.3` with the release you want to install. A failed download or checksum check leaves your current executable untouched. The installer does not modify your shell configuration or saved login.

### Windows

Download the Windows binary in PowerShell, then verify it before installation. This example selects x86-64; replace `amd64` with `arm64` for Windows on ARM:

```powershell
Invoke-WebRequest https://zorveus.com/cli/latest -OutFile zorveus-version.txt
$version = (Get-Content zorveus-version.txt -Raw).Trim()
$binary = "zorveus-windows-amd64.exe"
$releaseUrl = "https://zorveus.com/cli/$version"
Invoke-WebRequest "$releaseUrl/$binary" -OutFile $binary
Invoke-WebRequest "$releaseUrl/SHA256SUMS" -OutFile SHA256SUMS
$checksumLine = Get-Content SHA256SUMS | Where-Object { ($_ -split '\s+')[1] -eq $binary }
if (@($checksumLine).Count -ne 1) { throw "Missing or duplicate release checksum" }
$expected = ($checksumLine -split '\s+')[0]
$actual = (Get-FileHash $binary -Algorithm SHA256).Hash
if ($actual -ne $expected) { throw "Checksum verification failed" }

$installDir = Join-Path $env:LOCALAPPDATA "Zorveus\bin"
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
Copy-Item $binary (Join-Path $installDir "zorveus.exe")
$env:Path = "$installDir;$env:Path"
zorveus --help
```

For new terminals, add `%LOCALAPPDATA%\Zorveus\bin` to your user's `Path` through Windows Environment Variables.

Login and credential creation use Keychain on macOS, Secret Service on Linux, or Credential Manager on Windows. Your credential store must be available and unlocked.

To build or contribute to the CLI, see [Building Zorveus](BUILDING.md).

## Your first app

To configure customer allowance plans through guided prompts, run `zorveus setup`. The wizard handles sign-in, app and model configuration, one or more plans, optional routing, review, and apply. A routing key uses the collected plan references as selectors. See [Set up customer allowance plans](docs/setup.md) for a walkthrough and non-interactive inputs.

Follow this path to create an app and learn the main workflow:

1. [Sign in](#sign-in). Choose your organization and request the permissions for the example. Confirm the account and organization with `zorveus whoami`.
2. [Configure an app](#configure-an-app). Create `zorveus.json`, replace its contents with the example, and validate it. Expect `Valid manifest`; no remote resources have been created by these commands.
3. [Review and apply changes](#review-and-apply-changes). Plan should propose creation of the app, inference key, cap, and router. Apply creates them and adds their IDs to the manifest.
4. [Check the result and make an update](#check-the-result-and-make-an-update). Confirm an empty plan, then change the app name and apply the update.
5. [Create a development copy](#create-a-development-copy). Confirm the new manifest has no resource IDs before applying it, and different IDs afterward.
6. [Import an existing app](#import-an-existing-app). Use the app ID from your manifest and choose a new destination file.

After that, try [BYOK](#bring-your-own-provider-key) and [service keys](#manage-service-keys), using the permissions included in the default login. A configured spending cap sets a limit; it does not purchase credits or send an inference request.

## Connect a coding agent

Sign in with `zorveus login`, then configure your MCP client to launch:

```sh
zorveus mcp serve --directory /absolute/path/to/your/project
```

The local stdio server exposes twelve tools for account and app inspection, manifest preparation, validation, planning, apply, status, and service-key management. It shares your saved CLI login and keeps credentials in the OS credential store. Apply requires approval of a saved plan; direct service-key revocation also requires explicit confirmation.

See [Connect an MCP client to Zorveus](docs/mcp.md) for client configuration, tool arguments, and the review-and-apply workflow.

## Sign in

```sh
zorveus login
zorveus whoami
zorveus apps list
```

Login opens the Zorveus dashboard, where you choose an organization and approve access. It uses `https://api.zorveus.com` and `https://app.zorveus.com` by default.

By default, login requests all CLI management permissions for apps, inference connections, routing keys, spending caps, service keys, and provider credentials. These include creation, updates, credential rotation, and supported deletion or revocation operations. Choose the organization and approve access in the browser. For a local server, use `zorveus login --local`.

To request fewer permissions, pass an explicit scope list. For example, to view apps only:

```sh
zorveus login --scope "apps:read"
```

The CLI keeps one saved login. Signing in again replaces it. To request different permissions or switch organizations, run `login` again. Your organization role still determines which operations you can perform.

If renewal fails or your session expires without renewal, an interactive command explains what happened and waits for Enter before opening browser sign-in. After sign-in, the command continues from where it stopped. Sign in with the same organization and user for an existing plan. Setup keeps your answers, and apply keeps its recorded progress. Press Ctrl+C to cancel. The CLI never retries a refresh token whose outcome is uncertain.

Commands with `--non-interactive`, commands without a terminal, and MCP tools return a login error instead. Run `zorveus login` from your terminal, then retry them.

Use `login --no-browser` to print the authorization URL and open it in your preferred browser. To end the CLI session:

```sh
zorveus logout
```

## Configure an app

For a complete starting configuration, choose one of the [example manifests](examples/README.md): a minimal app, an internal tool, subscription tiers, business credits, OAuth consent, OpenAI BYOK, or a service-key backend.

Create a starter manifest:

```sh
zorveus init --name "Assistant"
```

`init` refuses to overwrite an existing file. For a separate exercise, use a new filename with `--config` and pass that filename to subsequent commands.

The default file is `zorveus.json`. New apps allow all models with `allowedModels: ["*"]`. To select specific models, repeat `--model` when running `init`.

Edit the manifest to describe the resources you need. This example creates an app, an inference key with a monthly USD cap, and a routing key that selects it:

```json
{
  "version": 1,
  "app": {
    "name": "Assistant",
    "allowedModels": [
      "*"
    ]
  },
  "keys": {
    "backend": {
      "name": "Backend",
      "cap": {
        "amount": "100",
        "currency": "USD",
        "period": "monthly"
      }
    }
  },
  "routingKeys": {
    "primary": {
      "name": "Assistant router",
      "members": [
        "backend"
      ],
      "defaultSelector": "backend"
    }
  }
}
```

Resource object keys connect manifest entries. A routing key's members refer to the names in `keys`, such as `backend`. Each inference key also works directly, without a router. Write monetary amounts as strings to preserve decimal precision.

Validate the file:

```sh
zorveus validate
```

`init`, `validate`, `clone`, and `promote` work offline without login. Validation checks the file's structure and references; planning checks remote resources and access.

See the [manifest reference](docs/manifest-v1.md) for OAuth settings, consent defaults, pricing defaults, key policies, and the full update rules.

## Review and apply changes

```sh
zorveus plan --config zorveus.json
```

Plan shows resource blocks with `+` for additions, `~` for updates, and `-` for removals or revocations. Updates show changed fields as `old -> new` and hide unchanged fields. The summary counts planned additions, updates, and removals. Plan saves these changes without changing remote resources.

Terminal output uses green, yellow, and red for these actions. Use `--no-color` or set `NO_COLOR` to disable colors. Piped output and `--json` results contain no color codes.

Review the output, then apply that saved plan:

```sh
zorveus apply --plan zorveus.plan.json --yes
zorveus status --config zorveus.json
```

Apply creates or updates the resources and writes their IDs back into your manifest. Keep those IDs when editing existing resources. Running plan again after a successful apply produces zero changes when the configuration matches.

The CLI saves newly generated secrets in your OS credential store. Secrets do not appear in the manifest, saved plans, or apply output. Saved credentials are associated with resource IDs, so later applies and display-name changes do not lose them.

To retrieve one secret, select its manifest reference or ID:

```sh
zorveus retrieve keys.backend
zorveus retrieve app
zorveus retrieve routingKeys.primary --config zorveus.dev.json
zorveus retrieve serviceKeys.customers --config zorveus.dev.json
zorveus retrieve aconn_example --config zorveus.dev.json
```

Use names and IDs from your own manifest. `retrieve` prints only the selected secret; `--json` returns its item, ID, and secret in the result envelope. It reads the local OS credential store and never rotates a key. See [Retrieve saved secrets](docs/setup.md#retrieve-saved-secrets) for copying values, scripts, and missing credentials.

If you edit the manifest or a planned remote value changes, run `zorveus plan` again before applying. A successful plan replaces the existing plan for that manifest at the same output path. If planning fails, the previous file is preserved. The default plan filename follows the manifest: `zorveus.dev.json` produces `zorveus.dev.plan.json` in the same directory. With `--from`, the filename follows the target `--config` file. Use `--out` to choose another path.

### Check the result and make an update

Check that the applied configuration matches:

```sh
zorveus status --config zorveus.json --json
zorveus plan --config zorveus.json --out check.plan.json
```

Expect `result.in_sync: true` in the status result and `No changes.` from plan. You can also check the app and its keys in the dashboard for the same organization.

Next, change only `app.name` in `zorveus.json` to `Assistant renamed`. Keep all resource IDs. Inspect the difference before applying it:

```sh
zorveus status --config zorveus.json --json
zorveus plan --config zorveus.json --out rename.plan.json
```

Status should report `in_sync: false` with the name difference. Plan should propose a name update without creating another app or rotating keys. Apply it and check again:

```sh
zorveus apply --plan rename.plan.json --yes
zorveus status --config zorveus.json --json
```

Expect `in_sync: true` again and the same resource IDs. The name update requires `apps:update`, which is included in the default login request.

### Remove managed resources

Omitted fields and resource sections preserve existing values. Removing a previously managed key or router from an explicit resource object proposes its revocation. An empty object removes all resources recorded for that section of the manifest. Unrelated resources are left alone.

Planned revocations, cap removal, and redirect removal need an additional flag:

```sh
zorveus apply --plan removal.plan.json --yes --allow-delete
```

Service keys and provider credentials belong to the organization. Removing their entries from a manifest leaves those shared resources intact.

### Resume an interrupted apply

Keep the saved plan and the `.zorveus/` directory beside your manifest, then repeat the same apply command:

```sh
zorveus apply --plan zorveus.plan.json --yes
```

The CLI uses its recorded progress to resume. If a resource ID is known but its generated secret was not saved, recovery can rotate the credential. If a creation or rotation outcome is unknown, apply stops to avoid repeating an uncertain operation. Inspect the resource and reported journal before continuing. An unfinished apply blocks a new plan for the same manifest.

## Import an existing app

```sh
zorveus import --app-id app_example --config imported.json
zorveus status --config imported.json
```

Replace `app_example` with the app ID from your manifest or dashboard. Import requires `apps:read`, `app_connections:read`, and `routing_keys:read`; the default login request already includes them.

Import writes the app's settings, active programmatic inference connections, and routing keys to a new manifest, preserving their IDs. It does not import secrets, customer connections, or shared organization credentials. The destination must not already exist.

Status reports changed fields, missing or revoked resources, and the latest apply result without changing anything. In JSON output, check `in_sync` to determine whether the remote configuration matches.

## Create a development copy

```sh
zorveus clone --from zorveus.json --to zorveus.dev.json --name "Assistant development"
zorveus validate --config zorveus.dev.json
zorveus plan --config zorveus.dev.json
zorveus apply --plan zorveus.dev.plan.json --yes
```

Clone preserves configuration and aliases while removing app, inference-key, and routing-key IDs. Applying the copy creates separate resources. The source is unchanged, and the destination must be new.

Clone rejects manifests containing service keys or provider credentials because those resources are shared across the organization. Prepare a manifest without those sections before cloning.

## Plan changes from development to production

Keep separate dev and prod manifests. Both use the same resource object names, and each keeps its own backend IDs. To bring development settings into an existing production app:

```sh
zorveus login
zorveus plan --from zorveus.dev.json --config zorveus.prod.json --keep-names
zorveus apply --plan zorveus.prod.plan.json --yes
```

Choose the production organization during login. The source supplies policies and settings; matching entries retain their production IDs. `--keep-names` also preserves their production display names. For example, `keys.backend` matches `keys.backend`, regardless of display names or JSON property order. New entries receive production IDs during apply.

Plan leaves both files unchanged. Apply writes the resolved settings and IDs into the production manifest. The source file stays unchanged. Editing either file after planning requires a fresh plan before starting apply.

Names copy from the source by default. Use `--keep-names` when production needs its own dashboard labels:

```sh
# Copy supplied settings, including display names. Keep production IDs.
zorveus plan --from zorveus.dev.json --config zorveus.prod.json

# Copy supplied settings while keeping production display names and IDs.
zorveus plan --from zorveus.dev.json --config zorveus.prod.json --keep-names
```

With `--keep-names`, new entries use the source name unless their target entry already supplies a name. To rename just a production resource, edit its `name` in the production file and run `zorveus plan --config zorveus.prod.json`. Callback URLs, caps, model lists, and credential settings still come from the source in both modes. The saved plan records your name choice; apply needs no extra flag.

Omitted optional fields preserve target values, except key model inheritance. An omitted resource section preserves that section. An explicit object supplies its desired entries, so removing a managed inference key or router can propose revocation. Review those changes and pass `--allow-delete` during apply when appropriate.

See the [cross-app planning guide](docs/promotion.md) for complete before-and-after JSON. Use `promote --from dev.json --to prod.json` only when you want to update the target JSON locally before planning.

## Bring your own provider key

Add a `providerCredentials` section to your app manifest:

```json
{
  "providerCredentials": {
    "openai": {
      "name": "OpenAI primary",
      "provider": "openai",
      "secretFromEnv": "OPENAI_API_KEY",
      "routingMode": "auto_resolve",
      "defaultModelPolicy": [
        "openai/gpt-4o-mini"
      ]
    }
  }
}
```

Set `OPENAI_API_KEY` in the environment of the shell running the CLI. The CLI does not automatically load `.env` files. Keep the actual key out of the manifest.

Provider management requires `provider_credentials:read` and `provider_credentials:write`, both included in the default login request. Run plan and apply after setting the environment variable. The model must also be allowed by your app and inference key. With `auto_resolve`, eligible connections in the organization can use the credential according to its model policy.

Provider metadata updates preserve the stored secret. A changed secret input proposes rotation; an unchanged input and active version produce no rotation. Status can compare the remembered input when its environment variable is available. An unavailable variable is reported as `unverified`.

Use `providerConfig` for nonsecret provider settings. Azure requires `api_base` and `api_version`; Vertex requires `project` and `location`. See the [shared credential rules](docs/manifest-v1.md#shared-organization-resources) for supported settings and secret formats.

## Manage service keys

Declare `serviceKeys` in a manifest to create or manage organization service keys. Each entry contains a name and machine scopes, or an existing ID to reuse. Newly generated secrets go to the OS credential store.

Service-key management uses `service_keys:read`, `service_keys:write`, and, for revocation, `service_keys:revoke`. These are included in the default login request. Service-key management requires organization-wide consent.

```sh
zorveus service-keys list
zorveus service-keys revoke --id service_example --yes
```

Revocation affects the key across the organization. Service-key payload scopes describe what the generated key can do; they are separate from the permissions granted to your CLI login.

## Use another environment

To connect to a local Zorveus server with its management API on port 8000 and dashboard on port 3000:

```sh
zorveus login --local
zorveus whoami
zorveus apps list
```

`--local` selects `http://localhost:8000` and `http://localhost:3000`. Subsequent commands use the saved login's API origin. Local login requests the same full set of CLI permissions by default. Use `--scope` to request fewer permissions.

For other environments, use `--api-url` and `login --dashboard-url`. Custom HTTP origins must use loopback addresses and require `--allow-insecure-loopback`.

The CLI saves the dashboard URL for sign-in recovery. If an older login uses a custom API origin, run `login` with both URLs once to save that dashboard URL.

## Shell completion

Load zsh completion in your current terminal:

```zsh
autoload -Uz compinit
compinit
source <(zorveus completion zsh)
```

For future terminals, add the `source` line to `~/.zshrc` after your shell's completion setup. If your shell already runs `compinit`, keep its existing setup. Bash, fish, and PowerShell are also supported:

```sh
zorveus completion --help
```

## Scripts and JSON output

Use `--json` for structured results and `--non-interactive` to prevent interactive login:

```sh
zorveus status --config zorveus.json --json --non-interactive
```

JSON results include `version`, `ok`, `command`, and either `result` or `error`. Diagnostics go to stderr. Status exits zero after a successful read even when configuration differs, so check `result.in_sync` in scripts.

Normal terminal errors show a readable message. Use `--log-level debug` when you need additional command diagnostics on stderr; debug output also excludes credentials.

| Exit code | Meaning |
| --- | --- |
| 0 | Command succeeded |
| 1 | Operational failure or incomplete logout/login replacement |
| 2 | Invalid arguments or manifest |
| 3 | Login or additional permissions required |
| 4 | Target mismatch, stale plan, or changed manifest |
| 5 | Incomplete apply or uncertain mutation outcome |

Login requires an interactive terminal and browser consent. Other commands can use a saved login with an available OS credential store. Renewal is optional during consent. When renewal is unavailable or fails, interactive commands wait for Enter before opening sign-in. Hosts without an OS credential store are not supported.

## Command reference

| Command | Purpose |
| --- | --- |
| `login`, `whoami`, `logout` | Manage your CLI session |
| `apps list` | List apps in the selected organization |
| `init`, `validate` | Create and check a manifest offline |
| `setup` | Configure customer allowance plans and optional routing, review, and apply resources |
| `retrieve` | Print a saved secret by manifest reference or resource ID |
| `plan`, `apply` | Review and apply configuration changes |
| `import` | Adopt an existing app into a manifest |
| `clone` | Prepare a separate app configuration |
| `promote` | Update a target JSON locally with source settings and target IDs |
| `mcp serve` | Expose account, manifest, provisioning, and service-key operations to a local MCP client over stdio |
| `status` | Compare the manifest with remote resources |
| `service-keys list`, `service-keys revoke` | Inspect and revoke organization service keys |
| `completion` | Generate shell completion scripts |

Run `zorveus <command> --help` for flags. See the [manifest reference](docs/manifest-v1.md) and [JSON Schema](internal/manifest/schema/v1.json) for configuration details.

## Development

See [Building Zorveus](BUILDING.md) for source builds, local development, tests, and platform binaries.
