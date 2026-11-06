# CLI manifest backend contract

The CLI is wired to the backend follow-up supplied on 6 November 2026. Service-key management, provider metadata updates, and offset pagination are implemented and covered by fake-API tests. Live CLI/backend verification remains deferred at the user's request.

No backend source, migration, seed, or client policy was changed by the CLI work.

## Service keys

| Operation | Existing route | Management scope |
| --- | --- | --- |
| List metadata | `GET /orgs/{org_id}/service-keys` | `service_keys:read` |
| Create | `POST /orgs/{org_id}/service-keys` | `service_keys:write` |
| Update metadata | `PATCH /orgs/{org_id}/service-keys/{id}` | `service_keys:write` |
| Rotate | `POST /orgs/{org_id}/service-keys/{id}/rotate` | `service_keys:write` |
| Revoke explicitly | `POST /orgs/{org_id}/service-keys/{id}/revoke` | `service_keys:revoke` |

Management scopes authorize these operations. The `scopes` field in a service-key payload contains the existing machine scopes, such as `app_connections:write` or `product_users:write`. The backend enforces the caller's role and ownership rules. Service-key management requires organization-wide consent and is rejected for consent restricted to individual apps.

Metadata updates send only supplied `key_alias` and `scopes` fields. An existing entry can use `name: null` to clear its alias. Omitted fields are preserved. New entries still require a nonempty name and scope list. Metadata changes preserve the resource ID and secret; changed scopes apply to the existing secret immediately.

Rotation sends no body. The flat response contains the same `org_service_api_key_id`, organization, replacement prefix, and one-time `service_key`. Rotation preserves metadata and expiry and invalidates the previous secret. The CLI records the confirmed identity and prefix and delivers the secret through the OS credential store.

When a created key's ID is known but its secret was not delivered, apply can recover through rotation. The journal records rotation intent before the request. A lost rotation reply produces `rotation_outcome_unknown` and blocks a second automatic rotation. A confirmed authorization rejection can be retried after access is restored.

Expired and revoked keys are not updated, rotated, or reactivated. Removing a manifest entry does not revoke the organization resource. Explicit revocation uses `zorveus service-keys revoke --id ... --yes`.

## Provider credentials

| Operation | Existing route | Management scope |
| --- | --- | --- |
| List metadata | `GET /provider-credentials?org_id=...` | `provider_credentials:read` |
| Create | `POST /provider-credentials?org_id=...` | `provider_credentials:write` |
| Update metadata | `PATCH /provider-credentials/{id}?org_id=...` | `provider_credentials:write` |
| Rotate secret | `POST /provider-credentials/{id}/rotate?org_id=...` | `provider_credentials:write` |

Metadata updates send a nonempty subset of `credential_name`, `routing_mode`, `default_model_policy`, and `provider_config`. Omitted fields are preserved. `provider_config` replaces the whole object; explicit `null` clears it only where the provider allows an empty configuration. Required Azure and Vertex settings are checked before writes, including when planning resolves the provider from an existing ID.

The CLI checks that metadata updates preserve the credential ID, provider, and active secret version. Request payloads never include secret, secret kind, provider identity, or version fields in a metadata PATCH. Changing the provider itself requires a new resource.

`secretFromEnv` supplies a secret only to a create or rotate operation. `secretKind` selects the existing API's secret format. Private input fingerprints and active version IDs prevent unchanged inputs from being rotated repeatedly. Plans, manifests, journals, and ordinary output contain no raw secret.

## Pagination

Connection and provider list requests use fixed filters and `limit` values while advancing `offset`. The CLI continues until it receives a page shorter than `limit`, including an empty page after an exactly full final page. Duplicate IDs across pages stop the read rather than returning a partial or inconsistent result.

Connection listing remains restricted to the selected organization, app, and programmatic connection type. Provider listing remains restricted to the approved organization. Every returned page is checked against those bounds.

Offset pagination does not provide a database snapshot across concurrent edits. Import and provider lookup no longer impose the previous 1,000-record limit.

## Rollout and app settings

The backend handoff requires `make zorveus-seed` to upgrade unchanged previous CLI registrations, followed by fresh consent for the service-key management scopes. Customized or disabled registrations are preserved, and existing tokens do not gain new permissions. Backend seeding remains the user's responsibility. No schema migration is needed for this contract.

App `oauth.clientType` and `allowedScopes` are creation-only. Existing values may be checked; changes are rejected before writes. No client-type conversion endpoint is requested.

Consent defaults, app pricing defaults, credit conversion rates, and redirect additions/removals continue to use existing app APIs.

No backend alias fields, routing-group IDs, session endpoint, manifest storage, or server-side apply state is needed.
