# Zorveus CLI and manifest proposal

Status: design proposal, corrected 6 November 2026

The proposed CLI provisions and updates Zorveus resources from a versioned JSON manifest. This document records the manifest examples, resource lifecycle, routing rules, clone behavior, and recommended implementation approach

The authentication milestone, `whoami`, and local manifest milestone are implemented in this standalone CLI workspace. CLI-side planning, apply, import, and status are implemented. Live backend verification remains deferred. See [the CLI README](README.md) for available commands and [the manifest reference](docs/manifest-v1.md) for the version 1 file contract

The default filename is `zorveus.json`. Separate files can configure separate apps, including production and development apps, without an environments section

## Agent implementation handoff

The local backend checkout is `/Users/peterakande/DevProjects/Backend/work/zorveus`. Read its `AGENTS.md` and inspect the current code before implementing the CLI. This is a machine-local path. If that checkout is unavailable, use the transferred backend checkout containing the current OAuth changes

Paths below are relative to the backend checkout:

| Area | Files to inspect |
| --- | --- |
| OAuth endpoints and payloads | `zorveus/routers/user_access_oauth_router.py`, `zorveus/schemas/user_access_oauth_schemas.py` |
| Authorization, token exchange, renewal, and revocation | `zorveus/services/user_access_oauth_service.py`, `zorveus/services/utils/user_access_oauth_policy.py` |
| Delegated request authentication | `zorveus/root/user_access_auth.py` |
| App listing and response models | `zorveus/routers/apps_router.py`, `zorveus/schemas/app_schemas.py` |
| OAuth and session persistence | `zorveus/database/db_handlers/oauth_db_handler.py`, `zorveus/database/db_handlers/session_db_handler.py` |
| Configuration and built-in client seeding | `zorveus/root/settings.py`, `zorveus/root/seed.py` |
| OAuth behavioral tests | `tests/zorveus/test_user_access_oauth_router.py`, `tests/zorveus/test_user_access_oauth_policy.py` |

Milestones 1 and 2 are implemented. Milestone 3 CLI plan, apply, import, and status are implemented against existing backend APIs and IDs; live backend verification is deferred. Routing aliases and apply bookkeeping stay in the CLI. Any missing backend permission or scope is reported to the user for them to add

The [CLI implementation notes](docs/provisioning-backend-audit.md) record the resource mappings and remaining work. The earlier backend changes and migration were reversed. They are not prerequisites for milestone 3

| Milestone | Deliverable | Completion check |
| --- | --- | --- |
| 1. Authentication and reads | Go executable with `login`, `apps list`, and `logout` | Log in through the existing frontend, select an organization there, list its apps, refresh an expired token when renewal was approved, and revoke the CLI login |
| 2. Local manifests | Versioned JSON Schema, `init`, offline `validate`, and offline `clone` | Validate the documented examples and reject invalid identities, aliases, references, decimals, unknown fields, and duplicate JSON keys. Cloning preserves the source and refuses an existing destination |
| 3. Provisioning | `plan`, saved-plan `apply`, then `import` and drift reporting | Use existing API reads and writes, resolve manifest aliases in the CLI, and test planning, local progress tracking, secret delivery, and recovery |
| 4. MCP | `mcp serve` over stdio | Call the same Go operations and reserve stdout for protocol messages |

Use a separate Go module in this `zorveus-cli` workspace, with the executable entry at `cmd/zorveus`. Pin tested dependency versions. Start with Cobra, `net/http`, `log/slog`, `golang.org/x/oauth2`, and the OS credential-store adapter. Add the JSON Schema validator during milestone 2 and the MCP SDK during milestone 4

Read the [user access OAuth plan](zorveus_user_access_oauth_plan.md) and [frontend handoff](zorveus_frontend_user_access_oauth_handoff.md) before implementing authentication. Check the current router and schema code when a document and implementation disagree

The OAuth backend is implemented. The frontend owns login, organization selection, and consent. Authorization URLs contain no organization ID. The token response returns `org_id`, which the CLI stores with the credentials. `apps list` sends that organization as `owner_org_id` to `GET /apps`. Use the existing `/me` response for user/session details. The selected organization and issued scopes come from the token response

Milestone 1 includes secure credential persistence, coordinated refresh, revocation, JSON command results, safe stderr diagnostics, and non-interactive behavior. Keep terminal interaction outside the reusable authentication and API operations. Small typed models for the OAuth and apps endpoints are sufficient initially. A general Go SDK is unnecessary

Test callback state validation, denial, PKCE exchange, token expiry, optional renewal, concurrent refresh coordination, login replacement, revocation, and organization isolation. Verify that errors and output exclude credentials. Inject HTTP and credential-store dependencies so tests do not alter a developer's real keychain

Verify the completed milestone against the local API and implemented frontend, including an API read after access-token expiry. Check that the executable builds with `CGO_ENABLED=0` for the intended release targets. Report cross-compilation separately from runtime verification of OS credential stores

The existing OAuth changes may be uncommitted in the checkout handed to the agent. Preserve that work and follow the repository's `AGENTS.md`. This handoff authorizes implementation starting with milestone 1, without a commit or push

## Milestone 3 backend contract

The backend changes requested on 6 November 2026 are limited to delegated access on existing app-connection operations, bearer authentication for `GET /me`, and routing-key display-name updates

| Operation | Existing endpoint | Required OAuth scope |
| --- | --- | --- |
| List connections | `GET /app-connections?org_id=...&app_id=...` | `app_connections:read` |
| Connection details and key metadata | `GET /app-connections/{id}` and `GET /app-connections/{id}/keys`, with `org_id` | `app_connections:read` |
| Create a programmatic connection | `POST /app-connections/dashboard` | `app_connections:write` |
| Update key metadata or policy | `PATCH /app-connections/{id}/keys/{key_id}` | `app_connections:write` |
| Rotate the inference key | `POST /app-connections/{id}/keys/rotate` | `app_connections:write` |
| Revoke the connection | `POST /app-connections/{id}/revoke` | `app_connections:revoke` |
| Update a connection cap | `PATCH /app-connections/{id}/cap` | `caps:app_write` |
| Update credit mode, overrun policy, or charge multiplier | Existing `credit-mode`, `overrun-policy`, and `product-user-pricing` PATCH routes under the connection | `caps:org_write` |
| Read the current user and session | `GET /me` | Any currently valid management access token |
| Rename a routing key | `PATCH /orgs/{org_id}/apps/{app_id}/routing-keys/{api_key_id}` with `{"key_alias":"Updated router name"}` | `routing_keys:write` |

Connection operations still require their existing role permissions. The backend resolves the actual connection to enforce the approved organization and app restrictions. App-restricted connection listing requires `app_id`. Non-default credit mode, overrun policy, or a charge-multiplier override during creation also requires `caps:org_write`

Run `make zorveus-seed` to add the connection scopes to an unchanged previous built-in CLI registration. Customized or disabled client registrations are preserved. Existing consents do not gain scopes automatically, so log in again to request the new permissions

## Decisions before implementation

Keep the manifest shape in these examples. The initial implementation adds precise update rules, authentication behavior, and command contracts

Start with a separate Go module in this `zorveus-cli` workspace, producing the `zorveus` executable. Put the executable entry in `cmd/zorveus` and add internal packages for commands, API access, authentication, and manifests as they are implemented. Provisioning operations use the existing backend endpoints. Do not add empty packages or implement MCP during the first milestone

The first milestone delivers `login`, `apps list`, and `logout`, with JSON output and non-interactive behavior. Local manifest commands follow in milestone 2. `whoami` verifies the existing `/me` user/session response using the saved management bearer token. Manifest `plan`, `apply`, `import`, and drift reporting use existing API reads and writes

### Existing API use

Reuse the current OAuth flow, `/me`, app management, connections, inference keys, routing keys, caps, and rotation endpoints. Check the exact request, response, and permission requirements in the existing implementation

`routingAlias` belongs to the manifest and CLI mapping. It resolves to an existing key and the routing membership's `selector_alias`. It does not require a field on backend connections

Routing keys use their existing `api_key_id`. If rotation returns another credential ID, the CLI records that returned ID. Do not add a routing-group ID system

The CLI owns manifest comparisons, saved plans, and local progress records. It does not require backend manifest storage, a new session endpoint, a versioned management API, or a separate plan-approval service

If an operation lacks an approved scope or permission, report the exact existing endpoint and required access. The user will make any needed backend scope changes. CLI implementation does not authorize changes to backend code, schema, client policy, or data

Unsupported manifest sections fail explicitly before a mutation rather than being silently skipped

### Manifest version 1 update rules

| Input | Behavior |
| --- | --- |
| Unknown field, duplicate JSON object key, or unsupported `version` | Reject with a location and error code before any API mutation |
| Missing resource `id` | Create a new resource and record its returned stable identity |
| Null or empty resource `id` | Reject. Remove the field to request creation |
| Changed `name` with the same `id` | Update the existing resource |
| Omitted optional field during update | Preserve its remote value, except the documented model-inheritance rule |
| Explicit `null` | Clear a field only when that field is nullable |
| Omitted `keys`, `routingKeys`, `serviceKeys`, or `providerCredentials` section | Preserve that section, including previously managed resources |
| Explicit resource array | Reconcile the supplied entries against the previously managed set. Preserve unrelated remote resources |
| Explicit empty resource array | Propose removal of previously managed app-owned resources, subject to the destructive-operation option. Shared organization resources require an explicit disposition |
| Omitted key `allowedModels` | Inherit `app.allowedModels`; this is an explicit exception to omission preserving remote values |
| Omitted routing `defaultSelector` | Use no default on creation and preserve the current default on update |
| Null routing `defaultSelector` | Clear the default so callers must select a member |
| Routing `members` | Require at least one member on creation. On update, omission preserves the list; an explicit list replaces it and may be empty if the default is also cleared |
| Decimal configuration | Require finite decimal strings and apply each field's backend bounds. Zero is not a supported cap amount |

Resolve file paths relative to the manifest's directory. Read provider secrets only through the explicit `secretFromEnv` field. Missing secret inputs fail before writes. Do not expand environment variables throughout arbitrary manifest values

The organization and API origin belong to the current CLI login and command options. They do not require an `environments` block in the manifest. Plans record that target binding, and apply rejects a mismatch with the current login

### Authentication behavior

`login` defaults to `https://api.zorveus.com` for management requests and `https://app.zorveus.com` for the dashboard. `--local` selects `http://localhost:8000` and `http://localhost:3000` and permits loopback HTTP. Explicit `--api-url` and `--dashboard-url` overrides take precedence. The dashboard override can also select `https://zorveus.com`. App reads and logout use the saved login's API origin unless an explicit target option is supplied

`login` uses the seeded public client and no client secret. The user selects an organization in browser consent. The CLI saves the `org_id` returned with the issued tokens. The CLI binds a numeric loopback listener to an available port before opening the browser. It sends the actual `/callback` URL during authorization and code exchange. Port `10000` in the registration is a placeholder

The first milestone defaults login to `apps:read`. An explicit `--scope "apps:read apps:update"` requests additional scopes for the intended operation. Do not request every scope allowed for the built-in client, since the user's role may not permit them. A command that needs more scopes reports the required authorization. A role-based denial does not trigger repeated login attempts

Generate fresh state and an S256 PKCE verifier for each login. Validate state on success and denial. Stop the listener after completion or timeout. The callback belongs to the CLI, not to the dashboard or local MCP server

Login requests renewal eligibility so the browser can offer continued access. The browser checkbox remains unchecked until the user chooses it. If no refresh token is returned, report that a new login will be needed after the short-lived access token expires. Use returned expiry information rather than assuming every token lasts a full 120 seconds

The CLI supports one saved login. A successful `login` replaces the previous login, including when the user chooses a different organization or API origin. Commands and the local MCP adapter use that current login. There are no saved login profiles or account-switching commands

Complete browser authorization before replacing the saved login. Denial, cancellation, or timeout preserves the previous login. Revoke the previous CLI session as part of replacement and report any revocation or credential-storage failure explicitly

Use one OS credential-store entry containing the tokens and their canonical API origin, selected organization, and OAuth client. These fields bind credentials to their target rather than identify separate saved logins. An unavailable credential store produces an actionable error without silently writing plaintext credentials

Unattended CI on a host without an available credential store requires a separate explicit credential-provider design. It is not promised by the first interactive-login milestone

The API client sends credentials only to the configured management origin, using HTTPS except for explicit local loopback development. Token-bearing API calls do not automatically follow redirects. Browser authorization navigation and the CLI's callback listener remain separate

The CLI and MCP share a cross-process credential lock for the single saved login. Login replacement, refresh, and logout use that lock. Inside the lock, reload the record before deciding whether to refresh. Persist both replacement tokens before releasing the lock. Another process uses that saved replacement rather than submitting the old refresh token again. Every token acquisition uses this coordinator, including any OAuth-library refresh path

Do not blindly retry refresh after an uncertain network outcome. The backend may have consumed the old credential while the response was lost. Recheck the credential store under the lock; if no replacement was saved, require login. The current backend cannot recover a lost refresh response

Logout uses the same lock, reloads the latest credentials, revokes the current refresh token when available or the current access token otherwise, and then clears the local record. If revocation fails, report incomplete logout and retain the credentials for retry. The dashboard session screen remains available for remote revocation

Use the existing `/me` response for verified user/session details with any valid management bearer token. The backend limits its organization list to the approved organization. Locally saved login metadata must be labeled as local state. Do not add a parallel session endpoint

### Command results and apply boundaries

| Exit code | Meaning |
| --- | --- |
| `0` | Command completed, including a plan that contains changes |
| `1` | Operational failure, including an unsupported backend capability |
| `2` | Invalid arguments or manifest |
| `3` | Login or renewed authorization required |
| `4` | Stale plan, conflicting remote state, or target mismatch |
| `5` | Apply is incomplete or a remote mutation has an unknown outcome |

JSON mode emits one versioned result object with `ok`, `command`, and either `result` or an `error` containing a stable `code` and safe `message`. Validation errors include a JSON Pointer when available. Diagnostics remain on stderr, and no result includes credentials

Non-interactive commands never open a browser or wait for terminal input. Interactive login opens the browser or prints the authorization URL if opening fails. Non-interactive login reports that interactive authorization is required

Apply consumes a saved plan, requires explicit acceptance in non-interactive use, and needs a separate destructive-operation option for planned deletion or revocation. Acceptance applies to that bound plan rather than newly computed changes

The intended command forms are:

~~~bash
zorveus login --scope "apps:read apps:update"
zorveus validate --config zorveus.json
zorveus plan --config zorveus.json --out zorveus.plan.json
zorveus apply --plan zorveus.plan.json --yes
zorveus apply --plan zorveus.plan.json --yes --allow-delete
~~~

The plan and apply forms remain unavailable until their CLI implementation is complete. `--yes` accepts the saved plan; `--allow-delete` separately permits its destructive operations

Preflight verifies the target, manifest digest, supported existing API operations, observed remote values, and secure delivery destinations for one-time credentials. Re-read the relevant resource before changing it and stop when it differs from the saved plan

Apply calls existing resource endpoints. It records completed steps and returned IDs locally. A failed or uncertain step stops dependent operations. It does not claim an atomic transaction across endpoints or an atomic check-and-write that the existing API does not provide

Write generated IDs to the manifest atomically. Refuse replacement if the file changed during apply. Preserve the returned IDs in local progress so a file-write conflict does not repeat creation

If an identified resource's secret response was lost, check the secure local destination, then use that resource's existing rotation API to obtain a replacement. Save it securely and record the returned credential ID. When the created resource cannot be identified, report that uncertainty and use available reads to resolve it. Never blindly repeat creation

### Accepting changes

OAuth supplies the existing backend permissions. The CLI shows the saved changes before application. Non-interactive execution requires `--yes`, and destructive actions also require `--allow-delete`

There is no additional backend plan-approval endpoint in this design. The existing API remains responsible for its authorization and billing rules

## Implementation brief

One Go executable provides the CLI and local MCP adapter. Both call shared typed Go operations, which use the ordinary HTTPS management API

~~~text
Coding agent
    |
    | MCP over stdio
    v
zorveus mcp serve
    |
    | Shared Go operations and short-lived management tokens
    v
Zorveus HTTPS management API
~~~

The backend does not need to become an MCP server for this design. Streamable HTTP remains an option for a future shared local or hosted MCP service. Local process-based integration uses stdio

| Responsibility | Proposed package or tool |
| --- | --- |
| Commands and flags | `github.com/spf13/cobra` |
| MCP protocol and stdio | `github.com/modelcontextprotocol/go-sdk/mcp` |
| Manifest JSON Schema validation | `github.com/santhosh-tekuri/jsonschema/v6` |
| HTTP and structured logging | Standard `net/http` and `log/slog` |
| OAuth login | `golang.org/x/oauth2` |
| OS credential storage | `github.com/zalando/go-keyring`, subject to platform verification |
| Release packaging | GoReleaser, used during release builds |

| Proposed command | Purpose |
| --- | --- |
| `login`, `logout`, `whoami` | Authorize through OAuth, revoke the CLI authorization, and inspect the current identity |
| `apps list` | Read apps in the organization selected during login |
| `init` | Create an initial manifest |
| `validate` | Validate the local file and its references |
| `plan` | Preview remote changes and save a plan |
| `apply` | Execute a valid plan and persist generated IDs |
| `status` | Show resource state, drift, and the last apply result |
| `import` | Write an existing app's configuration to a manifest |
| `clone` | Prepare a new manifest without app-owned resource IDs |
| `mcp serve` | Run the local MCP adapter over stdio |

Manifest commands accept `--config`. Structured command results use `--json`, and non-interactive execution uses `--non-interactive`. MCP mode reserves stdout exclusively for protocol messages

OAuth consent grants the CLI management access on behalf of the approving user. The backend issues a short-lived user-delegated management access token and enforces current user permissions, approved scopes, resource bounds, expiry, and revocation. Renewal requires explicit consent

The local MCP adapter uses the CLI's normal authenticated client and credential store. There are no agent-specific authorization commands or separate task-grant credentials. Any renewal stays within the OAuth consent, and the model never receives backend bearer tokens

Logging uses `slog` to stderr, with structured JSON in MCP mode and an optional protected local log file. Use the backend audit and request identifiers that existing endpoints provide. Local apply progress belongs to the CLI. Token values, secrets, authorization headers, and raw manifests are excluded from logs

## Manifest and identity

One manifest describes one app and its managed resources. Optional sections include `keys`, `routingKeys`, `serviceKeys`, and `providerCredentials`

`version` identifies the manifest schema. Amounts, credit conversion rates, and charge multipliers use decimal strings. The CLI preserves them without conversion through binary floating-point values

A resource has a generated `id` and an editable `name`. An inference key also has a `routingAlias`. These fields have separate responsibilities

`id` identifies the existing managed resource for updates. A new resource omits it, and the CLI records the generated ID after creation. IDs are safe to commit. Raw credentials are never written into the manifest

`name` is a display label. Changing it updates the same resource

`routingAlias` is the selector application code sends and routing keys reference. Aliases are unique among keys in the manifest, with uniqueness checked by the CLI. The CLI resolves them to existing credential IDs within the selected app and organization

Changing an alias retains the resource ID but requires updating routing memberships and application callers. Array position and display names never determine identity

The examples use illustrative IDs. Use the existing connection and credential IDs returned by the backend. Rotation can return a new credential ID; the CLI updates its stored reference to that ID

`keys[].id` represents the existing `app_connection_id`. `routingKeys[].id` represents the existing routing `api_key_id`. Resolving current credential IDs and updating references after rotation is CLI bookkeeping

Keys inherit `app.allowedModels` when their own `allowedModels` is omitted. An explicit key list must be a subset of the app list. The CLI resolves inheritance for planning, and the backend validates the resulting policy

## Standalone keys and routing keys

Inference keys are standalone resources. They own model permissions, caps, credit modes, overrun policies, and charge multiplier overrides

A routing key references an explicit subset of those keys through their aliases. A normal key remains usable directly and can belong to multiple routing keys

The selected key supplies the connection and billing policy. A routing key has no separate cap, credit balance, or charge multiplier. Referencing the same key through another router does not create another allowance or reset usage

Creating a normal key does not attach it to existing routers. Removing a membership leaves the normal key intact. Deleting a router leaves its member keys intact. Updating a key's policy affects every router referencing it

Members must be active programmatic inference keys in the same app and organization. Routing keys cannot contain other routing keys, service keys, or user OAuth keys

`defaultSelector` is optional. A new router without a default, or a router with an explicitly null default, requires callers to select a member. Omission during an update preserves the existing default. An unknown selector or inactive member fails without selecting another route

The business backend chooses selectors from trusted subscription state. A routing credential authorizes selection of any configured member and must remain in trusted server code

Selectors already belong to routing memberships. The CLI resolves manifest aliases to those existing selectors and key IDs. Alias uniqueness is checked within the manifest, without a new backend connection field

## Internal tool

One app and one standalone key are sufficient when no routing group is needed

~~~json
{
	"version": 1,
	"app": {
		"id": "app_internal",
		"name": "Internal assistant",
		"allowedModels": ["<model-id>"]
	},
	"keys": [
		{
			"id": "key_backend",
			"name": "Backend",
			"routingAlias": "backend",
			"creditMode": "enforce",
			"cap": {
				"amount": "100",
				"currency": "USD",
				"period": "monthly"
			}
		}
	]
}
~~~

Without product-user attribution, the cap covers combined unattributed usage through this key. With attribution, the connection cap supplies each product user's base allowance, subject to existing allowance-window and cross-connection accounting rules

An organization-wide safety cap is a separate control. A key cap must not be described as an organization wallet budget

## Free and Pro subscriptions

Standalone keys define each tier's policy. One routing key exposes both through their aliases

~~~json
{
	"version": 1,
	"app": {
		"id": "app_assistant",
		"name": "Writing assistant",
		"allowedModels": [
			"<standard-model-id>",
			"<premium-model-id>"
		]
	},
	"keys": [
		{
			"id": "key_free",
			"name": "Free plan",
			"routingAlias": "free",
			"allowedModels": ["<standard-model-id>"],
			"creditMode": "enforce",
			"overrunPolicy": "limit_output",
			"cap": {
				"amount": "1",
				"currency": "USD",
				"period": "monthly"
			}
		},
		{
			"id": "key_pro",
			"name": "Pro plan",
			"routingAlias": "pro",
			"creditMode": "enforce",
			"overrunPolicy": "limit_output",
			"cap": {
				"amount": "10",
				"currency": "USD",
				"period": "monthly"
			}
		}
	],
	"routingKeys": [
		{
			"id": "key_router",
			"name": "Application backend",
			"members": ["free", "pro"],
			"defaultSelector": null
		}
	]
}
~~~

The backend selects `free` or `pro` from its verified subscription data. Missing selection fails because no default is configured

Subscription prices and payment collection remain in the business's billing system. The manifest defines the AI allowance and access policy for each tier

## Business credits, charge multipliers, and multiple routers

An app can define a business-credit conversion rate and a default customer allowance charge multiplier. Individual keys can override the multiplier

~~~json
{
	"version": 1,
	"app": {
		"id": "app_business",
		"name": "Business assistant",
		"allowedModels": ["<model-id>"],
		"creditUnitsPerUsd": "1000",
		"productUserChargeMultiplier": "2"
	},
	"keys": [
		{
			"id": "key_starter",
			"name": "Starter plan",
			"routingAlias": "starter",
			"creditMode": "enforce",
			"overrunPolicy": "limit_output",
			"cap": {
				"amount": "5",
				"currency": "USD",
				"period": "monthly"
			}
		},
		{
			"id": "key_business",
			"name": "Business plan",
			"routingAlias": "business",
			"creditMode": "enforce",
			"overrunPolicy": "carry_overrun_forward",
			"productUserChargeMultiplier": "1.5",
			"cap": {
				"amount": "25",
				"currency": "USD",
				"period": "monthly"
			}
		}
	],
	"routingKeys": [
		{
			"id": "key_primary_router",
			"name": "Primary backend",
			"members": ["starter", "business"],
			"defaultSelector": null
		},
		{
			"id": "key_business_router",
			"name": "Business integration",
			"members": ["business"],
			"defaultSelector": "business"
		}
	],
	"serviceKeys": [
		{
			"id": "service_key_billing",
			"name": "Billing backend",
			"scopes": [
				"product_users:read",
				"product_users:write"
			]
		}
	]
}
~~~

Starter inherits the app's 2x allowance charge multiplier. Business uses 1.5x. These multipliers affect attributed customer allowance consumption without multiplying the organization's wallet charge

The base allowances correspond to 5,000 and 25,000 business credits at the configured app rate. Actual remaining allowance depends on recorded customer charges, reservations, adjustments, and usable grants

Both routers reference the same Business key and share its connection and billing state. The second router does not grant a second allowance

`carry_overrun_forward` selects the existing policy for completed usage that exceeds the available allowance. Selecting another policy must preserve the backend's existing settlement rules

Individual purchases and promotional grants remain runtime operations. Applying a manifest must never create purchases, issue credit grants, replenish spent credits, or settle usage

After verifying a purchase, the business backend can send this existing credit-grant API payload

~~~json
{
	"app_id": "app_business",
	"external_user_id": "customer_123",
	"external_reference": "purchase:payment_456",
	"amount": "2",
	"currency": "USD",
	"credit_units_per_usd": "1500",
	"source": "purchase"
}
~~~

This grants 3,000 extra credits at the purchase's saved rate. Repeating the same reference and matching request recovers the same grant. Conflicting parameters under that reference fail

Base allowance is consumed before eligible grants. Grants retain their saved rates and expiry rules. Updating the app default must preserve existing grants with saved rates and historical settled charges

Customer subscription assignment and allowance start times are runtime data. The business keeps a stable external customer ID and selects the current tier. Eligible allowance increases can request the existing `prorate_increase` behavior. Subscription payment proration remains in the business's billing system

## Bring your own provider key

Provider credentials can be declared with an environment-variable reference

~~~json
{
	"version": 1,
	"app": {
		"id": "app_documents",
		"name": "Document processor",
		"allowedModels": ["<openai-model-id>"]
	},
	"providerCredentials": [
		{
			"id": "credential_primary",
			"name": "Primary provider account",
			"provider": "openai",
			"secretFromEnv": "OPENAI_API_KEY",
			"routingMode": "auto_resolve",
			"defaultModelPolicy": ["<openai-model-id>"]
		}
	],
	"keys": [
		{
			"id": "key_processor",
			"name": "Processing backend",
			"routingAlias": "processor",
			"creditMode": "enforce",
			"cap": {
				"amount": "50",
				"currency": "USD",
				"period": "monthly"
			}
		}
	]
}
~~~

The manifest contains the environment-variable name, never the actual secret. The CLI reads the value only for an authorized credential operation and excludes it from plans, logs, errors, and ordinary JSON output

Provider credentials belong to the organization. Automatic resolution can affect other apps, so the plan must identify the shared scope and clone must require an explicit reuse or replacement choice

BYOK usage still consumes reference-priced allowance. Wallet fallback and aggregate organization caps remain separately managed organization settings. BYOK platform fees and wallet fallback retain their existing funding rules

## Users connect their own wallets

An OAuth app configures allowed access and consent defaults. Each user separately approves their own wallet connection

~~~json
{
	"version": 1,
	"app": {
		"id": "app_connected",
		"name": "Connected assistant",
		"allowedModels": ["<model-id>"],
		"allowedScopes": [
			"inference:write",
			"models:*"
		],
		"oauth": {
			"clientType": "public",
			"redirectUris": [
				"https://example.com/oauth/callback"
			]
		},
		"consent": {
			"defaultCap": {
				"amount": "10",
				"currency": "USD",
				"period": "monthly"
			},
			"minimumCapAmount": "1",
			"maximumCapAmount": "50",
			"allowUnlimitedCap": false
		}
	}
}
~~~

Applying this configuration does not authorize a user's wallet or bypass app review. User OAuth keys cannot become members of business routing groups

## Development copy

Clone prepares a new manifest with app-owned resource IDs removed. Aliases and routing references stay usable before new IDs exist

~~~json
{
	"version": 1,
	"app": {
		"name": "Writing assistant development",
		"allowedModels": [
			"<standard-model-id>",
			"<premium-model-id>"
		]
	},
	"keys": [
		{
			"name": "Free plan",
			"routingAlias": "free",
			"allowedModels": ["<standard-model-id>"],
			"creditMode": "enforce",
			"cap": {
				"amount": "1",
				"currency": "USD",
				"period": "monthly"
			}
		},
		{
			"name": "Pro plan",
			"routingAlias": "pro",
			"creditMode": "enforce",
			"cap": {
				"amount": "10",
				"currency": "USD",
				"period": "monthly"
			}
		}
	],
	"routingKeys": [
		{
			"name": "Application backend",
			"members": ["free", "pro"],
			"defaultSelector": null
		}
	]
}
~~~

Applying this file creates a separate app and records fresh IDs. Production and development manifests can use the same aliases because alias resolution belongs to each manifest

Copying a manifest verbatim retains its IDs and targets the same resources. File names alone do not establish isolation

## Updates and saved state

Planning compares the desired manifest with current resources from existing API reads. The CLI keeps its managed-resource mapping and completed apply steps locally. Do not add backend manifest-state storage

An explicit ID targets an existing resource and must match its expected kind, app, and organization. An absent ID requests creation. The CLI never adopts an existing resource merely because its name or alias matches

Schema validation and reference validation run before remote mutations. Planning resolves model inheritance, checks alias uniqueness, and identifies creates, updates, removals, and drift

Unrelated dashboard changes can survive a manifest update. Conflicting changes to the same managed field require an explicit decision. Apply reads the relevant remote values again before changing them and rejects an observed mismatch. This does not make separate read and write requests atomic

For example, changing a cap amount preserves the connection, secret, usage history, and credit balances

~~~text
Update key "Backend"
  cap.amount: 20 -> 50

Create: 0
Update: 1
Delete: 0
~~~

Omitted optional settings preserve remote values unless the schema explicitly defines inheritance. Clearing a nullable setting requires explicit null. An explicit list replaces that managed list, including routing memberships. These rules must be documented per field in the eventual schema

Removing a previously managed resource proposes removal rather than silently orphaning or deleting it. Revocation or deletion requires an explicit destructive-operation option. A removed routing membership is an update to the router and does not delete the member key

If an ID disappears while a previously managed resource is also removed, the plan shows the new creation and old removal. It must not guess that a matching name means a rename or silently replace an active key

Each apply records completed steps and returned resource IDs locally. Re-running apply checks that progress and the current remote resources before continuing. An unknown create outcome is resolved through existing reads or reported for review instead of being repeated

Manifest ID updates use atomic file replacement and reject a destination that changed during apply. Returned IDs remain available in the CLI's local progress record

One-time secrets require an explicit secure delivery destination before creation, such as an OS credential store or a protected local file excluded from Git. Local progress and ordinary plan output contain only safe identifiers. An identified resource with an undelivered credential is recovered through its existing rotation API. The CLI records the replacement ID and saves the secret securely. It does not rotate unrelated credentials

## Clone and import

The proposed clone workflow prepares a file, previews creation, and provisions the new resources

~~~bash
zorveus clone --from zorveus.prod.json --to zorveus.dev.json --name "Assistant development"
zorveus plan --config zorveus.dev.json --out zorveus.dev.plan.json
zorveus apply --plan zorveus.dev.plan.json --yes
~~~

Clone leaves the source file untouched and refuses to overwrite an existing destination. It copies desired app configuration and clears IDs for app-owned resources being cloned

Clone is an offline file operation and requires no login or organization selection. The current login used for planning determines the target organization. Planning validates every retained shared-resource reference against that organization

Organization provider credentials and service keys are shared resources. Define an explicit reuse or replacement choice before implementing clone support for those sections. Without that choice, clone rejects the file rather than retaining or clearing their IDs silently. A reused resource remains shared. Replacement requires new provisioning and secure delivery of any generated credential

Removing a shared-resource entry from an app manifest does not implicitly delete or revoke the organization resource. A destructive plan must name the shared resource and its organization-wide effect. Shared organization settings are not copied as app resources

Clone excludes actual secrets, customer records, usage history, wallet balances, and credit grants

Import reads an existing app and writes a manifest with populated IDs and aliases. It never exports raw secrets. Routing aliases are chosen or resolved in the imported manifest using existing memberships. Import does not add aliases to backend connections

## CLI language and packaging

The recommendation is Go, distributed as a standalone `zorveus` executable. This follows a comparison of Python, TypeScript, and Go, with installation independent of a separately managed language runtime given the highest weight for the distributed CLI

The backend stays Python. The CLI calls typed management APIs and validates a versioned manifest contract. Existing billing calculations and permissions stay on the backend. Manifest reconciliation and apply progress stay in the CLI

Go builds include the Go runtime, so customers running a release executable do not need Go, Python, Node.js, or a language package manager installed. The executable still targets a specific operating system and architecture and uses the host's operating-system facilities

Dependencies must support the intended standalone build. Prefer pure-Go dependencies and build with `CGO_ENABLED=0` where supported. Disabling cgo is not a blanket claim that every operating system has no library or service requirements. Verify the actual release artifacts, including TLS trust and secure credential storage, on supported platforms

The following comparison distinguishes default distribution paths from optional interpreter bundling

| Criterion | Python | TypeScript on Node.js | Go |
| --- | --- | --- | --- |
| Default release path | Python package and interpreter | npm package and Node.js | Platform executable with Go runtime included |
| Existing backend validation tooling | Pydantic directly | Shared JSON Schema integration | Shared JSON Schema integration |
| Existing typed provisioning coverage in audited SDKs | 0 of 5 required resource families | 0 of 5 required resource families | Requires a Go management client |
| Official MCP SDK tier | Tier 1 | Tier 1 | Tier 1 |
| Existing client transport reuse | Python SDK transport | TypeScript SDK transport | Go standard library and typed API client |
| Primary additional maintenance | Python packaging and tool environments | npm packaging and Node compatibility | Go client, schema integration, and platform releases |

Python and Node.js can also ship applications with an included interpreter. PyInstaller and Node single-executable applications provide those options. Runtime-independent installation is therefore not exclusive to Go. Go's normal compilation model makes that distribution approach a direct fit, without adding an interpreter-bundling workflow to this project

The SDK audit inspected five provisioning resource families: apps, inference keys, routing keys, service keys, and provider credentials. Neither existing Python nor TypeScript service client exposes those typed resources. Python exposes product users, and TypeScript exposes product users and usage events. All choices require the new management authentication and provisioning contracts

The earlier SDK dependency counts were three direct runtime dependencies for Python and zero for TypeScript. Those counts describe only the existing SDKs, not complete CLIs, installed size, or vulnerability exposure. They do not decide this choice

Python offers the most direct alignment with existing Pydantic validation. TypeScript offers the npm distribution path. Go offers a direct executable for developers and agents working in projects of any language, without requiring installation of another language runtime

The versioned manifest JSON Schema is embedded in the Go binary and validated locally. Existing backend endpoints validate their own payloads. A new backend manifest-contract module is unnecessary

Custom validation rules that JSON Schema cannot express remain authoritative on the backend. Shared accepted and rejected manifest fixtures verify agreement across the CLI and backend where their validation overlaps

Use typed Go request and response models for management operations. Generate them from OpenAPI where practical and review the generated contract. Keep the client focused on CLI operations rather than creating a general-purpose Go SDK before it is needed

The proposed starting tools are Cobra for command parsing, the standard HTTP client for API transport, a JSON Schema validator supporting draft 2020-12, Go's testing tools, and GoReleaser for packaging. Select and pin exact tested versions during implementation

Maintain the CLI as a separate Go module, with command handling separate from API access, manifest validation, secure credential storage, and provisioning operations. Terminal prompts and output formatting stay outside the reusable operations so a later MCP adapter can use the same code

An official Tier 1 Go MCP SDK is available. A local adapter can expose tools from the executable or invoke its structured commands. A hosted adapter uses the same scoped management API without relying on a developer's local login state

The initial proposed release matrix is macOS, Linux, and Windows on amd64 and arm64, producing six target executables before archives and checksums. Test each supported target and document minimum OS requirements. Cross-compilation does not replace testing OS-specific credential stores and login behavior

Release maintenance includes versioned artifacts, SHA-256 checksums, appropriate signing, macOS notarization, and package-manager integration where useful. CI downloads pinned tools by full versioned URLs, verifies official checksums, and never pipes downloaded scripts into a shell

Rebuild affected executables for Go runtime and linked dependency security updates. Customers replace the executable through a documented upgrade path. Changes to a bundled runtime are delivered through our releases

The existing Terraform provider under `terraform/provider` already has a Go module and a GoReleaser configuration using cgo-disabled builds. That is a reference for repository tooling, not proof that its release matrix or implementation should be copied into the Zorveus CLI

Decimal arithmetic is not a deciding factor. Every implementation preserves decimal strings, while the backend calculates charges. The Go client must not route monetary configuration through binary floating-point values

There is no completed CLI to benchmark. Binary size, cold installation time, warm startup time, and plan latency remain unmeasured. Record those on supported targets during implementation instead of assigning unsupported performance percentages

The language choice prioritizes a runtime-independent developer tool over direct Python validation-library reuse. Python remains reasonable if matching the backend becomes more important than standalone distribution, and TypeScript remains reasonable if npm-only distribution becomes the dominant requirement. For the CLI scoped in this proposal, Go is the recommended implementation

## User access OAuth plan

The dedicated [user access OAuth implementation plan](zorveus_user_access_oauth_plan.md) is the source of truth for CLI and local MCP authentication. The backend, Go CLI authentication, and offline manifest commands are implemented. CLI provisioning is implemented; live backend verification and local MCP remain pending

OAuth login issues a user-delegated management access token, not an organization service key. The authenticated principal identifies the user, OAuth client, selected organization, authorization, approved resources, and issued scopes

Effective permissions are the intersection of the user's current organization permissions, client-allowed scopes, approved consent, and issued token scopes. User suspension, membership removal, role reduction, or revoked consent removes the corresponding access

The only new table is `oauth_clients` for management-client registration. Extend existing `user_sessions` with browser and headless types and store the current access hash, optional refresh hash, and separate credential expiries directly on each row

The backend creates a session for each independent login. The CLI retains one login and replaces its previous login when authorization succeeds again. Renewing credentials updates the existing session row without changing its session ID. Existing OAuth request and code tables retain consent and one-time codes with explicit management purpose and client linkage

The backend seeds the built-in CLI with stable public client ID `zrv_client_zorveus_cli` after migration to `head`. Repeated seeds preserve an existing registration, including disabled status and custom policy. Management requests and codes reference `oauth_clients` through `oauth_client_id`, with `app_id` null. Business apps remain managed resources in `developer_apps` and receive no CLI registration fields or rows

The Go CLI uses browser Authorization Code with PKCE. The proposed token lifetime is 120 seconds. Optional renewal preserves user and client identity and cannot exceed consented permissions or lifetime. The local stdio MCP adapter uses the same authentication client

Users see browser and headless logins in session management and can revoke an individual login to invalidate its current access and refresh credentials. Browser cookie authentication remains type-specific

Refresh replacement compares and atomically replaces the currently stored hash. A consumed token no longer matches and is rejected. Historical refresh hashes, theft detection through token reuse, and automatic session revocation after such reuse are deferred

Organization service keys remain independent machine credentials in `org_service_api_keys`. Inference credentials retain their existing behavior. Delegated service-key provisioning remains future work and requires an explicit credential-creation policy. The current browser-only service-key routes cannot be called using the CLI's user access token

Provisioning resolves aliases in the CLI, uses the existing resource IDs, and records local apply progress. Required backend scope additions are left to the user

## Command behavior and implementation order

The planned command set appears in the implementation brief. The initial provisioning stage includes login lifecycle commands, `init`, `validate`, `plan`, `apply`, `status`, `import`, and `clone`. The `mcp serve` adapter follows the same management-authentication foundation and reuses the CLI's OAuth authorization

Commands support structured JSON output and non-interactive execution. JSON mode writes one documented result to stdout, sends diagnostics to stderr, and never includes secret values. Non-interactive mode fails on missing input instead of waiting for a prompt

A saved plan records the target account, manifest digest, proposed operations, and the remote values observed while planning. Apply validates that binding before execution. A plan must not carry secrets

Implementation starts with milestone 1 authentication and app reads. Milestone 2 adds the versioned schema, offline validation, clone, and fixture-based behavioral tests before remote provisioning exists

Milestone 3 adds CLI alias resolution and typed calls to existing APIs. Then implement planning, saved-plan apply with local progress, and read-only import in the order defined by the milestone table

The first apply supports apps, standalone inference keys, caps, and routing memberships with retry recovery and secure secret delivery. Follow with pricing defaults, service keys, BYOK credentials, and OAuth configuration using the same engine

Verification must cover renames without recreation, alias collisions within a manifest, alias reuse across manifests, shared members without duplicate allowance, rotation preserving references, stale-plan rejection, cross-organization rejection, clone isolation, interrupted creation recovery, and secret exclusion

Offline tests use injected API and credential-store dependencies. End-to-end verification exercises the real management API and a funded local gateway where billing behavior is involved

MCP follows after the CLI workflow works end to end. The initial local adapter is the `mcp serve` subcommand, using stdio and calling the shared Go operation functions directly. A hosted adapter uses the same authenticated management API, with user identity isolated per request

Initial MCP tools expose validation, planning, application of an authorized plan, and status. OAuth consent remains a user-facing authorization step. An unauthenticated adapter reports that login is required rather than inventing a separate agent authorization command. The adapter does not expose arbitrary shell execution or an unrestricted HTTP client

## Logging and transport

Normal commands write results to stdout and diagnostics to stderr. In `--json` mode, stdout contains one documented result object. In MCP stdio mode, stdout contains only MCP protocol messages, including during startup and failures

Use `--log-level` for error, warn, info, or debug diagnostics. Default to info, with no secret-bearing debug mode. Use text diagnostics for an interactive terminal and structured JSON for MCP. An explicit `--log-file` writes structured diagnostics to a private file without creating persistent local logs by default

Local diagnostics identify the command or tool, request ID, operation ID, safe credential identifier, duration, and safe error code. Backend audit records identify the authenticated actor, OAuth client and authorization, resource, authorized action, safe changes, and outcome. Correlation IDs connect a CLI failure to backend evidence

Do not log bearer tokens, renewal credentials, authorization codes, PKCE verifiers, provider secrets, authorization headers, or full request and response bodies. Provisioning can return one-time credentials, so response logging and generic object dumps are especially unsuitable

The agent launches the executable as a local subprocess and uses stdio. That process then calls the normal HTTPS management API. MCP transport and backend API transport are separate connections

Streamable HTTP can also run on localhost, but it requires a listening service and additional local connection handling. The older HTTP+SSE transport is a legacy predecessor. Streamable HTTP can itself carry SSE responses, so SSE is not another new transport required by this design

If a shared HTTP MCP service is added later, it needs its own request authentication, origin and host validation, loopback binding for local use, and per-user credential isolation. The initial stdio adapter requires no listening MCP port

## Billing boundary and remaining gap

The manifest configures resources and policies. Customer subscription assignments, checkout, payments, grants, refunds, and usage settlement remain authenticated runtime operations

Strict prepaid usage with zero included allowance is not supported by the current positive-cap validation. Omitting a cap means unlimited allowance, so it cannot represent purchased-credits-only access. Zero-base support needs a separate backend decision and regression coverage before the CLI promises it

## Sources

Existing behavior is described in the [finance model](zorveus_finance_model_guide.md), [routing implementation](zorveus_routing_keys_plan.md), [credit units contract](zorveus_allowance_and_credit_units_contract.md), [charge multiplier design](zorveus_product_user_charge_multiplier_plan.md), and [allowance periods](zorveus_product_user_allowance_period_plan.md)

Current authentication and identity constraints are visible in [app management](../zorveus/routers/apps_router.py), [routing management](../zorveus/routers/routing_keys_router.py), [routing schemas](../zorveus/schemas/routing_key_schemas.py), and [credential rotation](../zorveus/database/db_handlers/api_key_db_handler.py)

The SDK audit inspected the sibling `zorveus-python` checkout's `pyproject.toml` and `src/zorveus/_service_client.py`, and the `zorveus-ts` checkout's `packages/sdk/package.json` and `packages/sdk/src/service-client.ts`

Language and distribution references, checked on 4 November 2026, are the official [Go build tutorial](https://go.dev/doc/tutorial/compile-install), [Go platform targets](https://go.dev/doc/install/source#environment), [cgo documentation](https://pkg.go.dev/cmd/cgo), [MCP SDK tiers](https://modelcontextprotocol.io/docs/sdk), [Pydantic JSON Schema documentation](https://docs.pydantic.dev/latest/concepts/json_schema/), [Go JSON Schema validator](https://github.com/santhosh-tekuri/jsonschema), and [GoReleaser build documentation](https://goreleaser.com/customization/builds/go/)

The alternative bundling approaches are described in the official [PyInstaller operating model](https://pyinstaller.org/en/stable/operating-mode.html) and [Node single-executable documentation](https://nodejs.org/api/single-executable-applications.html)

The local adapter and authorization references are the official [MCP transport overview](https://modelcontextprotocol.io/specification/latest/basic/transports), [Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk), [structured Go logging](https://pkg.go.dev/log/slog), [OAuth client package](https://pkg.go.dev/golang.org/x/oauth2), [OS keyring adapter](https://github.com/zalando/go-keyring), and [OAuth for native applications](https://www.rfc-editor.org/rfc/rfc8252.html)

The management authorization and renewal design follows the [OAuth security best current practice](https://www.rfc-editor.org/rfc/rfc9700.html) and [OAuth token revocation standard](https://www.rfc-editor.org/rfc/rfc7009.html)
