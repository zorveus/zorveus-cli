# Zorveus CLI

The CLI provides `login`, `whoami`, `apps list`, and `logout`, offline `init`, `validate`, and `clone`, saved `plan` and `apply`, and `import`, `status`, and `service-keys` commands. MCP remains planned. See the [CLI and manifest proposal](zorveus_cli_and_manifest_proposal.md).

Milestone 3 uses the existing backend APIs and IDs. Routing aliases and apply bookkeeping belong in the CLI. See the [CLI implementation notes](docs/provisioning-backend-audit.md). Any missing backend scope is reported to the user for them to add.

## Build and run

Install Go 1.26 or newer, then build:

```sh
go build -o bin/zorveus ./cmd/zorveus
./bin/zorveus --help
```

Log in to production:

```sh
./bin/zorveus login
./bin/zorveus whoami
./bin/zorveus apps list
./bin/zorveus logout
```

Login uses `https://api.zorveus.com` and opens `https://app.zorveus.com` for organization selection and consent. Use `--api-url` or `--dashboard-url` to override either origin, including `--dashboard-url https://zorveus.com`. The default scope is `apps:read`. Request additional permissions explicitly:

```sh
./bin/zorveus login --scope "apps:read apps:update"
```

Use `login --no-browser` to print the authorization URL and open it in your chosen browser. This still requires an interactive terminal.

The CLI saves one login in the OS credential store. Logging in again revokes the previous CLI session and replaces it, including when switching organizations or API origins. Denied, canceled, or timed-out browser authorization leaves the saved login unchanged.

If revocation or storage fails during replacement, the error explains the outcome. If the previous session was revoked before saving failed, you must log in again. A session whose refresh response was lost may need revocation from the dashboard.

## Local development

With the backend on port 8000 and dashboard on port 3000:

```sh
./bin/zorveus login --local
./bin/zorveus whoami --json
./bin/zorveus apps list --json
./bin/zorveus logout --json
```

`--local` selects `http://localhost:8000` and `http://localhost:3000` and permits HTTP on loopback origins. Explicit URL overrides take precedence. Without `--local`, custom HTTP loopback origins require `--allow-insecure-loopback`. Use consistent browser hostnames for local dashboard and API cookies. The CLI selects an available port on a numeric loopback address for its callback.

`whoami`, app reads, and logout use the saved login's API origin, so you do not need to repeat `--local`. Passing `--local` explicitly to those commands checks that the saved login targets the local API.

`whoami` verifies user and session details through the existing `GET /me` endpoint. It accepts any valid management login and renews tokens when needed. The selected organization and issued scopes come from the token response, rather than the user's default workspace in `/me`. Credentials are excluded from both human-readable and JSON output.

The updated backend handoff defines `app_connections:read`, `app_connections:write`, and `app_connections:revoke`, alongside the existing caps and routing scopes. New consent is required to add scopes to a saved login. See the [backend contract](docs/provisioning-backend-audit.md) for the operation mapping.

## Local manifests

Create and validate a manifest, then prepare a development copy:

```sh
./bin/zorveus init --name "Assistant" --model model-id
./bin/zorveus validate
./bin/zorveus clone --from zorveus.json --to zorveus.dev.json --name "Assistant development"
./bin/zorveus validate --config zorveus.dev.json --json
```

Use `--config` with `init` or `validate` to select another filename. Plain `init` defaults to `allowedModels: ["*"]`, allowing all models. Supply `--model` to choose specific models, repeating it for multiple entries. These commands work without login or an API connection, including with `--non-interactive`.

Validation uses the embedded [version 1 JSON Schema](internal/manifest/schema/v1.json) and checks duplicate JSON keys, resource identities, aliases, routing references, model subsets, decimal bounds, and consent cap ranges. Errors include a stable code and JSON Pointer. Remote IDs, organization ownership, model availability, and backend capabilities require later planning. Partial updates that depend on existing models or routing members report those checks in `deferred_checks`.

Clone removes app, inference-key, and routing-key IDs. It preserves aliases, routing references, decimal strings, explicit nulls, and omitted sections. The source stays untouched, and both `init` and `clone` refuse existing destinations. Clone requires enough configuration to create a new app; an incomplete update manifest cannot always be cloned.

Clone currently rejects nonempty `serviceKeys` or `providerCredentials` sections because their shared organization resources need an explicit reuse or replacement policy. Offline commands never expand environment variables or read provider secrets. See the [manifest field rules](docs/manifest-v1.md) for update semantics and validation limits.

## Plan and apply

Plan reads the selected app and its resources, compares the supplied fields, and saves changes without making remote writes:

```sh
./bin/zorveus plan --config zorveus.json --out zorveus.plan.json
./bin/zorveus apply --plan zorveus.plan.json --yes
```

Review the saved plan before applying. Use `plan --json` to get the proposed before and after values and the required scopes. Login still defaults to `apps:read`; request the scopes listed by your plan. Creating an app, keys, a cap, and a router typically needs:

```sh
./bin/zorveus login --scope "apps:read apps:create apps:rotate_secret app_connections:read app_connections:write caps:app_write routing_keys:read routing_keys:write"
```

App updates require `apps:update`. Connection credit mode, overrun policy, and charge-multiplier changes require `caps:org_write`. Connection revocation requires `app_connections:revoke`. The backend also enforces your role and consent restrictions. Log in again to add scopes to an existing consent.

Apply checks the target user and organization, source manifest, local state, and planned remote values before writing. It creates the app first, then declared organization resources, then inference connections and caps, then routing keys. Router aliases resolve to the connections' current inference key IDs. Returned resource IDs are written to the manifest when apply finishes.

One-time secrets are saved in the OS credential store under service `com.zorveus.cli.resources`. Apply reports each account name, using `<operation-id>/<resource-reference>`. Plans, manifests, journals, logs, and ordinary output contain no raw credentials. On macOS, use Keychain Access to inspect the saved entries. Headless systems need an unlocked OS credential store before creating resources.

Apply progress and alias mappings live beside the manifest in `.zorveus/`. Keep that directory when retrying. Run the same `apply --plan ... --yes` command after an interruption. If an app, inference connection, routing key, or service-key ID was received but its secret was not saved, apply uses the existing rotation endpoint. A lost service-key rotation reply stops recovery with `rotation_outcome_unknown`; repeating the saved plan does not trigger another rotation. If creation succeeded but no ID was received, apply stops with exit code 5 and does not repeat creation. Inspect the backend and local journal to resolve that resource before continuing. A new plan for the same manifest is blocked while its apply remains unfinished.

Omitting `keys` or `routingKeys` preserves the corresponding resources. An explicit array manages the resources previously recorded for that manifest; removing a recorded ID proposes revocation. Unrelated resources are left alone. Revocations, cap removal, and redirect removal require `--allow-delete` as well as `--yes`.

Apply supports app names and models, OAuth client type on creation, redirect URLs, consent defaults, credit conversion rates and charge-multiplier defaults, inference-key policies, connection billing settings and caps, routing names and memberships, service-key creation and reuse, and provider-credential creation and reuse. Service-key names and scopes can be updated in place. Provider metadata updates cover names, routing policies, model policies, and configuration. Existing app client type and allowed scopes remain creation-only, with changes rejected before writes. See the [backend contract](docs/backend-follow-up.md).

## Import and status

Import an existing app into a new file, then inspect its current state:

```sh
./bin/zorveus import --app-id app_example --config imported.json
./bin/zorveus status --config imported.json
./bin/zorveus status --config imported.json --json
```

Import needs `apps:read`, `app_connections:read`, and `routing_keys:read`. Connection and provider listings follow offset pagination beyond 1,000 records. It reads active programmatic connections and routing keys, preserves existing IDs and selectors, and records their CLI alias mapping. It refuses an existing destination. If the backend uses conflicting selectors that cannot fit one unique alias per connection, import stops without rewriting routes or creating a partial manifest.

Import includes app OAuth settings, consent and pricing defaults, key caps and policies, and routing memberships. It excludes raw secrets, customer connections, and organization-wide service keys and provider credentials whose ownership cannot be inferred from one app. Declare shared resources explicitly by ID when you want to reuse them.

Status reads resources without saving a plan or changing the backend, manifest, or local ownership state. It shows changed fields, missing or revoked resources, pending managed-resource removals, and the latest local apply result. Successful status reads exit zero even when resources differ; automation can inspect `in_sync` in JSON output. Status does not create a `.zorveus` directory.

## Shared credentials

Declare `serviceKeys` with a name and scopes to create a service key, or provide an ID to check and reuse one. The CLI saves newly returned secrets in the OS credential store. The CLI uses delegated service-key routes with `service_keys:read`, `service_keys:write`, and `service_keys:revoke`. These require organization-wide consent. Management scopes authorize the operation; each payload's `scopes` contains machine permissions. Run the backend's registration seed and obtain fresh consent as described in the [backend contract](docs/backend-follow-up.md) before live verification.

```sh
./bin/zorveus service-keys list
./bin/zorveus service-keys revoke --id service_example --yes
```

Provider credentials need `provider_credentials:read` and `provider_credentials:write`. A declaration reads its secret from the named environment variable:

```json
{
  "name": "Azure primary",
  "provider": "azure",
  "secretFromEnv": "AZURE_API_KEY",
  "providerConfig": {
    "api_base": "https://example.openai.azure.com",
    "api_version": "2024-02-01"
  }
}
```

Azure requires `api_base` and `api_version`. Vertex requires `project` and `location`. Optional `secretKind` accepts `api_key`, `service_account_json`, or `azure_ad_token`; its default is `api_key`. Provider configuration contains nonsecret settings. Secrets belong only in the variable referenced by `secretFromEnv`.

Apply checks required variables before any remote write. It stores an input fingerprint and the observed credential version privately in the OS credential store. An unchanged source and version produce no rotation. A changed input, an externally changed version, or adoption without a remembered fingerprint proposes explicit rotation. Provider metadata updates preserve the active secret version. `providerConfig` replaces the whole object; explicit null clears it only where the provider permits an empty configuration. Input fingerprints do not appear in plans or journals. Retries require the same input value that started that apply.

Status compares a declared provider input with the private fingerprint when its environment variable is available. It reports `unverified` when the input is unavailable and sets `in_sync` to false. It never prints the value or fingerprint.

Omitting shared entries, including through an explicit empty array, leaves organization resources intact. Clone continues to reject nonempty shared-resource sections until an explicit reuse or replacement choice is implemented. See the [backend contract](docs/backend-follow-up.md) for metadata updates, rotation recovery, and rollout requirements.

## Shell completion

For zsh, load completion in your current terminal:

```zsh
autoload -Uz compinit
compinit
source <(./bin/zorveus completion zsh)
```

You can now complete commands and flags with Tab, such as `./bin/zorveus apps l<Tab>` and `./bin/zorveus login --loc<Tab>`.

To enable it in future terminals, add these lines to `~/.zshrc`, using the absolute path to your executable:

```zsh
autoload -Uz compinit
compinit
source <(/absolute/path/to/zorveus-cli/bin/zorveus completion zsh)
```

If your shell setup already runs `compinit`, add only the `source` line after it. Bash, fish, and PowerShell are also supported. Run `./bin/zorveus completion --help` for available shells and each subcommand's help for setup instructions. Completion works without login or API requests and writes shell source rather than a JSON command result.

## Output and automation

`--json` writes one versioned result object to stdout. Diagnostics go to stderr. Credentials never appear in command results. For example:

```json
{"version":1,"ok":true,"command":"zorveus apps list","result":{"organization":"org_example","apps":[],"limit":1000,"may_have_more":false}}
```

Use `--non-interactive` to prevent browser interaction. Login requires an interactive terminal. Reads and logout work without terminal input when a usable credential store is available. Hosts without an OS credential store are not supported yet.

`apps list` returns up to 1,000 apps by default. Use `--limit` to request fewer. The API has no cursor pagination; `may_have_more` reports when the response reaches the requested limit.

| Exit code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Operational failure or incomplete logout/login replacement |
| 2 | Invalid arguments or manifest |
| 3 | Login or additional authorization required |
| 4 | Target mismatch, stale plan, or changed manifest |
| 5 | Incomplete apply or uncertain mutation outcome |

Use `--log-level debug`, `info`, `warn`, or `error` to control stderr diagnostics. There is no secret-bearing debug mode.

## Credentials and renewal

macOS uses Keychain through `/usr/bin/security`. Linux uses a Secret Service credential store through D-Bus. Windows uses Credential Manager. The CLI never falls back to plaintext storage.

Credentials use service `com.zorveus.cli` and account `current-login`. A private file in the user cache directory coordinates login replacement, refresh, and logout across CLI processes. The lock file contains no credentials.

Renewal is optional during browser consent. Without approval, login is required after access-token expiry. With approval, commands refresh near expiry and save both replacement tokens under the credential lock. If a refresh response is lost or cannot be saved, the CLI requires login instead of retrying a potentially consumed refresh token. Logout retains credentials when revocation fails.

## Code and verification

Read the code in this order:

1. `cmd/zorveus/main.go` connects the concrete dependencies.
2. `internal/cli/command.go` defines commands, flags, and safe output.
3. `internal/auth/login.go` handles browser authorization and login replacement.
4. `internal/auth/service.go` handles identity and app reads, renewal, and logout.
5. `internal/api/client.go` sends typed management requests without redirects.
6. `internal/auth/store.go` implements credential storage and file locking.
7. `internal/cli/manifests.go` connects the offline commands to `internal/manifest`.
8. `internal/api/me.go`, `management.go`, and `routing.go` map the existing user/session, resource-read, and routing-rename contracts.
9. `internal/provision/plan.go` compares resources; `apply.go`, `create.go`, and `write.go` execute saved steps and recover credentials.
10. `internal/provision/import.go` adopts existing app resources; `status.go` reports drift, and `shared.go` handles organization resources.

Run the behavior tests and static checks:

```sh
go test -race ./...
go vet ./...
```

Tests use injected stores and local HTTP servers. They never modify the developer's real keychain. Coverage includes PKCE, callback state, denial, login replacement, concurrent refresh, lost responses, revocation failures, target isolation, redirect rejection, and secret exclusion.

Manifest tests validate the proposal examples and the accepted and rejected fixtures in `internal/manifest/testdata`. They also cover source preservation, destination conflicts, exact decimal text, and offline command execution without authentication dependencies.

Cross-compile the six initial release targets:

```sh
sh scripts/check-release-builds.sh
```

Cross-compilation does not verify browser integration, TLS trust, or credential-store behavior on those operating systems. Those require runtime checks before releases.
