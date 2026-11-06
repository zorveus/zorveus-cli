# CLI provisioning implementation notes

The implementation uses the existing Zorveus APIs and resource IDs. Routing aliases, manifest reconciliation, and local apply bookkeeping belong in the CLI. The user owns any required backend scope changes

## Agreed backend contract

The user's updated [backend handoff](/Users/peterakande/DevProjects/Backend/work/zorveus/docs/zorveus_cli_and_manifest_proposal.md) adds delegated access to existing connection operations, management bearer authentication for `/me`, and routing-key display-name updates

| Operation | Existing endpoint | Required scope |
| --- | --- | --- |
| List connections | `GET /app-connections?org_id=...&app_id=...` | `app_connections:read` |
| Read connection details or key metadata | `GET /app-connections/{id}` or `GET /app-connections/{id}/keys`, with `org_id` | `app_connections:read` |
| Create a connection | `POST /app-connections/dashboard` | `app_connections:write` |
| Update inference-key metadata or policy | `PATCH /app-connections/{id}/keys/{key_id}` | `app_connections:write` |
| Rotate an inference key | `POST /app-connections/{id}/keys/rotate` | `app_connections:write` |
| Revoke a connection | `POST /app-connections/{id}/revoke` | `app_connections:revoke` |
| Update a connection cap | `PATCH /app-connections/{id}/cap` | `caps:app_write` |
| Update credit mode, overrun policy, or charge multiplier | Existing connection `credit-mode`, `overrun-policy`, and `product-user-pricing` PATCH routes | `caps:org_write` |
| Verify user and session details | `GET /me` | Any valid management access token |
| Rename a routing key | `PATCH /orgs/{org_id}/apps/{app_id}/routing-keys/{api_key_id}` with `key_alias` | `routing_keys:write` |

Connection operations retain the existing role checks and the grant's organization/app restrictions. App-restricted connection lists require `app_id`. Creating a connection with non-default credit mode, overrun policy, or a charge-multiplier override also requires `caps:org_write`

The backend's `make zorveus-seed` upgrades an unchanged previous default CLI registration while preserving customized or disabled registrations. Existing consents retain their issued scopes. Backend seeding and policy updates remain the user's responsibility

`GET /me` preserves the existing response and limits the organization list to the approved organization. The CLI uses the token response for its selected organization and issued scopes

## Implemented CLI calls

`whoami` calls `/me` through the normal saved-login and renewal flow. Typed resource reads cover apps, connections, key metadata, attached caps, and routing keys. The routing rename call sends only `key_alias` and checks that the response retains the requested credential ID

`plan` and `apply` now use these reads and the existing create, update, rotate, and revoke endpoints. Tests use injected HTTP transports and stores. They do not change the backend or the developer's real credential store. `import` and `status` now use those same reads without remote writes

## Existing resource mapping

| Manifest value | Existing API representation |
| --- | --- |
| `app.id` | `app_id` |
| `keys[].id` | Existing `app_connection_id`; current inference credentials are read through that connection |
| `keys[].routingAlias` | CLI-local alias used to resolve routing membership selectors |
| `routingKeys[].id` | Existing routing credential `api_key_id` |
| `routingKeys[].members` | Resolve aliases to current `selected_api_key_id` values and `selector_alias` values |

When rotation returns a new credential ID, the CLI records that returned ID and updates its local references. It does not require a new backend identity system

Session information uses the existing `/me` implementation. Access failures are reported using the existing login and authorization errors

## Planning and apply

Planning reads current resources through existing endpoints and compares them with the manifest. It saves proposed changes and the relevant observed values locally. Before the first write, apply reads those resources again and stops if the planned values have changed

Apply uses existing create, update, rotation, and revoke calls. It records returned IDs and completed steps locally and writes manifest IDs atomically. It stops on an unknown mutation outcome instead of blindly repeating a create

For an identified resource whose secret was not delivered, use its existing rotation API and save the returned secret securely. When the resource identity is unknown, report the uncertainty and resolve it through available reads before rotating or creating anything

Import and status reuse those same reads and the CLI's alias mapping. No new backend-owned alias, routing-group ID, manifest-state service, plan-approval endpoint, or versioned management API is required for this implementation

App OAuth creation, redirects, consent defaults, pricing defaults, shared service-key creation and reuse, explicit service-key revocation, and provider creation and rotation are implemented in the CLI. The service-key bearer routes, stable-ID rotation recovery, service-key metadata updates, provider metadata updates, and offset pagination are now wired to the supplied follow-up. App client type and allowed scopes remain creation-only. The exact operations, scopes, and rollout steps are listed in the [backend contract](backend-follow-up.md). Usage and retry behavior are documented in the [README](../README.md#plan-and-apply).

## Removed backend work

On 6 November 2026, the added backend source changes were reversed. The local development database was downgraded from `20261105_0067` to `20261105_0066` after checking that both added columns contained no values. The CLI client-policy addition was undone, with a compensating audit entry

The backend's pre-existing handoff documents were preserved. The incorrect patches and their implementation documents were removed from this workspace. The typed CLI reads now use the existing routing-key endpoint and no longer depend on the removed session endpoint or backend identity fields
