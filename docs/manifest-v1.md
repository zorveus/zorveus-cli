# Manifest version 1

One manifest describes one app. The default file is `zorveus.json`. Offline commands validate its local configuration; they do not provision resources or establish whether remote resources exist.

The [JSON Schema](../internal/manifest/schema/v1.json) defines fields, types, formats, enums, and creation requirements. The CLI embeds that schema and never downloads a schema specified by the manifest's optional `$schema` field. That field is for editor configuration.

## Resource identity and updates

`version` is `1`, and `app` is required. Optional resource sections are `keys`, `routingKeys`, `serviceKeys`, and `providerCredentials`.

IDs contain 1 to 128 ASCII letters, digits, underscores, or hyphens, starting with a letter or digit. They are opaque resource identifiers. Offline validation checks syntax and duplicates. Later planning must verify the resource kind, existence, app, and organization. App-owned IDs require an existing `app.id`.

An omitted ID requests creation. An explicit null or empty ID is invalid. A new app requires `name` and `allowedModels`. A new key requires `name`. A new router requires `name` and at least one member. Existing resources can omit display names to preserve them. Display names never identify resources.

Each resource section is an object keyed by a stable local name, such as `keys.backend` or `routingKeys.public`. Names use 1 to 64 lowercase letters, digits, underscores, or hyphens. The object key identifies the configuration entry; `id` identifies the backend resource; `name` is its editable display name. Separate `alias` and `routingAlias` fields are not accepted.

`plan --from dev.json --config prod.json` resolves source settings against target IDs by matching object keys. Planning leaves both files unchanged. Apply writes the resolved configuration and returned IDs into the target file. Source IDs are never copied. The target must contain an existing app ID. See the [cross-app planning guide](promotion.md).

| Input | Intended update behavior |
| --- | --- |
| Omitted resource section | Preserve previously managed resources in that section |
| Explicit resource object | Reconcile its entries with the previously managed set, preserving unrelated resources |
| Explicit empty object | Propose removal of previously managed resources, subject to later destructive-operation and shared-resource policies |
| Omitted optional field | Preserve the remote value, except key model inheritance |
| Explicit null | Clear the field only where the schema permits null |

Nullable fields are `app.creditUnitsPerUsd`, key `cap` and `productUserChargeMultiplier`, routing `defaultSelector`, consent `defaultCap`, `minimumCapAmount`, and `maximumCapAmount`, existing service-key `name`, and provider `providerConfig`.

Null sections, null IDs, and null model lists are invalid. Clone preserves omitted fields, explicit nulls, and empty objects as separate inputs.

## Models and routing

Model lists are nonempty arrays of distinct, nonblank model identifiers. `init` defaults to `["*"]`, allowing all models. Explicit `--model` options replace that default. Offline validation also accepts identifiers such as the proposal's `<model-id>` placeholders; model availability is a remote check.

Keys inherit `app.allowedModels` when their own list is omitted. An explicit key list must be a subset of the app's list. App model `*` permits any key model. If an existing app's model list is omitted, validation defers model-policy checks to planning.

The object key of each inference key is its routing selector. For example, `keys.backend` is selected as `backend`. These names stay in the manifest and CLI bookkeeping. The CLI resolves them to existing credential IDs and uses the existing routing-membership API.

Router `members` remains an array of distinct inference-key names, with at most 100 entries. Members reference the declared `keys` object. With an existing `app.id` and an omitted `keys` section, unresolved names are deferred to planning. An explicit `keys` object, including `{}`, requires references to resolve within that object.

An omitted `members` list preserves an existing router's membership. A new router requires at least one member. An existing router can set `members` to an empty array only with an explicit `defaultSelector: null`.

An explicit default selector must identify a member. If the existing membership is omitted, checking a new default is deferred. An omitted default preserves it during an update and selects no default during creation. Explicit null clears it.

## Decimal strings and caps

Amounts, conversion rates, and multipliers use plain decimal strings. Numeric JSON values, exponents, signs, whitespace, NaN, and infinity are rejected. For example, use `"1.25"`, not `1.25` or `"1e2"`.

| Field | Bounds |
| --- | --- |
| Cap amounts and consent minimum or maximum | Greater than zero, up to 12 whole digits and 12 fractional digits |
| `creditUnitsPerUsd` | Greater than zero, up to 12 whole digits and 12 fractional digits |
| `productUserChargeMultiplier` | At least 1, up to 12 whole digits and 6 fractional digits |

Leading zeroes and trailing fractional zeroes do not count toward precision bounds. Their original text is preserved. Decimal strings are limited to 128 characters. The CLI uses exact rational comparisons and never converts these values through binary floating-point numbers.

A cap has `amount`, a three-letter uppercase `currency`, and `period` of `daily`, `monthly`, or `lifetime`. The consent minimum cannot exceed its maximum, and a supplied default must fall within supplied bounds. Omitted remote bounds still need validation during planning.

The JSON Schema checks decimal string syntax. Exact numeric bounds, cap comparisons, and relationships between resources are additional CLI checks. The existing backend endpoints validate their own request payloads.

## Shared organization resources

Service keys and provider credentials belong to the organization. An omitted ID creates a new resource. A new service key requires a name and scopes. A new provider credential requires a name, provider, and `secretFromEnv` reference. 

Service-key metadata and provider metadata updates preserve their IDs and secrets. A null name clears an existing service-key alias; a new key requires a nonempty name. Provider configuration replaces the whole object, and null is permitted only when the provider does not require configuration. 

The schema lists supported providers and routing modes. Optional `secretKind` maps to the backend secret kind and requires `secretFromEnv`. Optional `providerConfig` accepts nonsecret `api_base`, `api_version`, `base_url`, `project`, and `location` settings. New Azure credentials require `api_base` and `api_version`; Vertex credentials require `project` and `location`.

`secretFromEnv` contains an environment-variable name, never a literal secret. Offline commands check its syntax without reading the variable or requiring it to exist. Apply resolves required secret inputs before remote writes and keeps private input fingerprints in the OS credential store. Plans contain only the source variable name and secret kind. Status reports whether a declared input matches the remembered input and active version; unavailable inputs are reported as unverified.

Clone rejects manifests with nonempty `serviceKeys` or `providerCredentials` objects. Their reuse or replacement policy is not implemented. Empty shared-resource objects remain empty.

## Validation output and limits

`validate --json` returns `valid`, schema `version` of `1`, `config`, and `deferred_checks` inside the usual command result. A successful offline validation is not permission to apply a configuration remotely.

Invalid manifests exit with code `2`. The JSON error contains `code`, `message`, and `pointer`. Pointers use JSON Pointer escaping. The empty pointer identifies the document root. Human-readable errors print the same location. Errors exclude manifest values and provider secrets.

Unknown fields and duplicate JSON keys are rejected, including escaped spellings of the same key. Files must be valid UTF-8, contain exactly one JSON object, fit within 4 MiB, and stay within 64 nesting levels.

`init` and `clone` create private files and refuse to overwrite files, directories, or symbolic links. Parent directories must already exist. Source and destination command paths are relative to the working directory unless absolute. Clone revalidates the result as a creation manifest after removing app-owned IDs.
