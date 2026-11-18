# Destroy a deployment

Use `zorveus destroy` to remove the app identified by a manifest, revoke its inference and routing keys, remove programmatic connection caps, and revoke every service key and provider credential listed with an ID in that manifest.

Deleting an app also revokes all its connections, including customer connections created outside the CLI. Listed service keys and provider credentials belong to the organization and may be used by other apps. Destroy includes those listed credentials. Other organization credentials are preserved.

## Preview the removals

```sh
zorveus destroy --config zorveus.dev.json --dry-run
```

The command displays the API origin, organization, resource IDs, and proposed removals. It saves `zorveus.dev.destroy.plan.json`. The preview does not change Zorveus resources. Use `--out` to select another plan filename.

Resource IDs come from the manifest and its existing CLI state. With an S3 backend, the CLI also restores missing IDs from shared state. It lists the app's programmatic connections and routing keys so app-owned resources created outside the manifest are included. An entry without an applied ID does not request creation during destroy.

## Confirm destruction

```sh
zorveus destroy --config zorveus.dev.json
```

Review the preview and answer the confirmation prompt. Its default is no. For scripts, pass `--yes` explicitly:

```sh
zorveus destroy --config zorveus.dev.json --yes --non-interactive
```

For your local Zorveus backend, add `--local` to both preview and destroy. `_state` continues to select storage independently of the API origin.

To execute the exact saved plan separately:

```sh
zorveus apply --plan zorveus.dev.destroy.plan.json --yes --allow-delete
```

Apply checks the manifest, target, state revision, and current resources again before starting. Changed inputs require a fresh plan. No organization resource is selected by display name alone.

## Permissions

Destroy needs the normal read scopes for the resources it inspects. Mutations use `routing_keys:write`, `app_connections:revoke`, `caps:app_write`, `service_keys:revoke`, `provider_credentials:write`, and `apps:delete` as needed. These scopes are included in the default CLI login request. Your organization role must also permit each operation.

All required scopes are checked before the first mutation. Destroy revokes routers first, removes programmatic connection caps and inference connections, removes listed organization credentials, and deletes the app last. Backend deletion is a soft deletion where supported. Deleting a stored provider credential does not revoke the underlying key at OpenAI or another external provider.

## Resume interrupted cleanup

Destroy uses the existing apply journal and state locks. It saves intent before each mutation and completion afterward. Rerun the same destroy command with the same manifest and output path to resume its saved plan. Completed steps are not repeated.

Resolve any unfinished provisioning apply before creating a destroy plan. Its recorded resource IDs must be reconciled before a different operation can take over.

With durable state, another checkout can resume without the local plan file:

```sh
zorveus apply --resume --config zorveus.dev.json --yes --allow-delete
```

Restore the reviewed manifest contents before resuming. Release a retained state lock only after confirming the previous process has stopped, following the [durable-state recovery guide](durable-state.md#inspect-and-release-locks).

The current app deletion API makes deleted apps inaccessible through its GET route. If its deletion response is lost, or its confirmation cannot be checkpointed, the CLI may be unable to distinguish deletion from lost access. It retains the unfinished journal and reports the error. It does not treat an access-denied response as confirmed deletion. See the [backend recovery follow-up](backend-follow-up.md#app-deletion-recovery).

## After completion

The manifest, recorded IDs, apply journal, and S3 object are retained as a record of the destroyed deployment. State marks the deployment as destroyed. Running destroy again produces no further resource mutations when the same resources remain removed.

To create the resources again, use the same manifest and backend:

```sh
zorveus plan --config zorveus.dev.json
zorveus apply --plan zorveus.dev.plan.json --yes
```

Plan ignores IDs recorded as removed by the completed destroy and proposes new resources from your current settings. Planning leaves the file unchanged. Apply records the replacement IDs and marks the deployment active again. The S3 bucket, object key, and state identity stay the same. Add `--local` when using your local API.

If an entry contained only an existing ID, add its creation settings to the same manifest before planning. For example, an app needs its name and model policy, and a provider credential needs its provider and `secretFromEnv`. Generated secrets are replaced during creation; provider secret inputs must still be available through the environment.

Destroy does not delete your bucket, local files, application deployment, OS credential-store entries, or Zorveus login. Stored application credentials have been revoked at Zorveus and cannot authorize further requests.
