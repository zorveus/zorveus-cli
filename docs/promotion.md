# Plan configuration between apps

Use the development JSON as the desired configuration and match resources against the production JSON to retain production IDs. Add `--keep-names` to preserve production display names too. Both files use version 1 resource objects. Planning leaves both files unchanged; apply updates only the production file.

## Prepare the target once

If production exists without a manifest, import it into a new file:

```sh
zorveus login
zorveus import --app-id app_production --config zorveus.prod.json
```

Select the production organization and replace `app_production` with its actual app ID. Import adopts active programmatic inference connections and routing keys. It does not import customer connections or shared organization credentials.

Use the same object names for matching roles:

| Resource | Matching path |
| --- | --- |
| App | One app per file; the target keeps `app.id` |
| Inference key | `keys.backend` |
| Routing key | `routingKeys.primary` |
| Service key | `serviceKeys.billing` |
| Provider credential | `providerCredentials.openai` |

The object name identifies the local configuration entry. Its `id` identifies the backend resource, and `name` is its editable display name. Separate `alias` and `routingAlias` fields are unnecessary in version 1.

Import retains existing routing selectors as inference-key object names where possible. Unrouted keys and routers receive generated labels such as `key_1` and `router_1`. Review those labels once so matching roles have matching object names in your two files. The CLI does not infer resource identity from display names.

## Development configuration

These IDs illustrate existing resources. Use your actual app IDs when trying the workflow. Development raises the backend cap and adds a worker:

```json
{
  "version": 1,
  "app": {
    "id": "app_development",
    "name": "Assistant dev",
    "allowedModels": [
      "*"
    ]
  },
  "keys": {
    "backend": {
      "id": "connection_dev_backend",
      "name": "Backend dev",
      "cap": {
        "amount": "200",
        "currency": "USD",
        "period": "monthly"
      }
    },
    "worker": {
      "id": "connection_dev_worker",
      "name": "Worker"
    }
  },
  "routingKeys": {
    "primary": {
      "id": "router_development",
      "name": "Assistant router dev",
      "members": [
        "backend",
        "worker"
      ],
      "defaultSelector": "backend"
    }
  }
}
```

## Existing production configuration

Production currently has only the backend key, with a smaller cap:

```json
{
  "version": 1,
  "app": {
    "id": "app_production",
    "name": "Assistant prod",
    "allowedModels": [
      "*"
    ]
  },
  "keys": {
    "backend": {
      "id": "connection_prod_backend",
      "name": "Backend prod",
      "cap": {
        "amount": "100",
        "currency": "USD",
        "period": "monthly"
      }
    }
  },
  "routingKeys": {
    "primary": {
      "id": "router_production",
      "name": "Assistant router prod",
      "members": [
        "backend"
      ],
      "defaultSelector": "backend"
    }
  }
}
```

## Review the production plan

```sh
zorveus login
zorveus plan --from zorveus.dev.json --config zorveus.prod.json --keep-names
```

Choose the production organization during login. Plan reads production through your saved management login. The source file supplies settings; its IDs never replace production IDs.

With `--keep-names`, expect one key creation, one cap update, and one router membership update:

- The app retains `app_production`.
- `keys.backend` retains `connection_prod_backend`, with a monthly USD 200 cap.
- `keys.worker` needs a new production credential.
- `routingKeys.primary` retains `router_production` and adds the worker member.

Both JSON files and remote resources remain unchanged during plan. The saved plan contains the resolved production configuration and fingerprints of both input files.

## Apply and repeat

```sh
zorveus apply --plan zorveus.prod.plan.json --yes
zorveus status --config zorveus.prod.json
```

Apply writes the desired configuration into `zorveus.prod.json`, preserving matched production IDs and recording the new worker's production ID. The source file stays unchanged. Repeating the same plan command after apply reports no changes.

Changing either file before starting apply requires a fresh plan. Once an apply has started, use the same saved plan to resume an interruption; its resolved configuration is already recorded. Keep the target manifest and its `.zorveus` recovery directory intact.

## Fields and removals

Supplied settings come from the source, including display names and callback URLs. Target IDs always stay in place. Choose whether names copy when you create the plan:

```sh
# Include the source display names.
zorveus plan --from zorveus.dev.json --config zorveus.prod.json

# Preserve the target display names.
zorveus plan --from zorveus.dev.json --config zorveus.prod.json --keep-names
```

Without the flag, the example also proposes renaming the production app to `Assistant dev`, its backend key to `Backend dev`, and its router to `Assistant router dev`. Their production IDs stay unchanged.

With `--keep-names`, an existing target's omitted name stays omitted, preserving its backend name. An explicit null service-key name also remains null. New entries use the source name unless their target entry already supplies a name. The saved plan records the flag, so apply uses the choice you reviewed.

Omitted optional fields preserve target values. OAuth and consent objects allow partial updates; `providerConfig` replaces its whole object. Explicit `null` clears nullable fields. An omitted key model list means inheritance from the resolved target app's model list.

An omitted resource section preserves the target section. An explicit object supplies the desired entries for that section. An unmatched source entry becomes a new resource without the source ID. An explicit empty object clears the target section.

Removing previously managed inference keys or routers can produce revocations. Those plans require `--allow-delete` during apply. Unrelated resources outside the managed set retain the normal plan behavior. Removing service-key or provider entries does not revoke those shared organization resources.

Object names should stay stable across environments. Renaming a display name in the target file preserves the match. Run a normal plan against that target to review and apply the rename. Changing an object name in only the source creates an unmatched entry and can propose creating a replacement and revoking the previous managed entry.

## Shared credentials

Service keys and provider credentials match by their object names and retain target IDs. New entries need their normal creation fields. Import does not adopt shared organization credentials, so declare existing target IDs under the appropriate object names when needed.

`secretFromEnv` copies an environment-variable name, never a secret value. Set the production value in the shell running plan and apply. Existing provider rotation and OS credential-store rules still apply. Offline promotion does not read these variables.

The existing backend restrictions remain in force. For example, changing an existing app's client type or allowed OAuth scopes fails during plan because the current app update API does not support those changes.

## Update a target JSON without deploying

For an offline file update before a separate plan:

```sh
zorveus promote --from zorveus.dev.json --to zorveus.prod.json --keep-names
```

This command updates the target JSON immediately, retaining its IDs. `--keep-names` also preserves existing target display names; omit the flag to copy source names. It validates the merged result and leaves the source unchanged. Use `plan --from` when you want the target JSON preserved until apply.
