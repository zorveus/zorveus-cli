# Share state through S3-compatible storage

Configure durable state when several checkouts or runners manage the same app. The CLI stores the last applied resource IDs and an apply journal in one S3 object. Plan, apply, status, import, and retrieve use the configured backend automatically.

The backend interface supports additional storage providers. S3-compatible object storage is the first implementation. Local state remains the default when no backend is configured.

## Configure a backend

Create a private bucket and choose one object key per app and environment. For AWS S3, enable bucket versioning so an operator can recover earlier snapshots. For another provider, use its backup or retention features where available. The CLI does not create buckets.

Add `_state` to your manifest. This field configures CLI storage and does not define a Zorveus resource:

```json
{
	"version": 1,
	"organizationId": "org_production",
	"_state": {
		"backend": "s3",
		"bucket": "zorveus-state",
		"key": "apps/assistant/prod.json",
		"region": "us-east-1"
	},
	"app": {
		"name": "Production assistant",
		"allowedModels": ["*"]
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
	}
}
```

Replace the organization, bucket, key, and region with your own values. Keep the same backend for every checkout managing this production app.

The CLI uses the official AWS SDK credential chain, including environment credentials, shared AWS configuration, and workload roles. `region` can be omitted when the SDK resolves it through `AWS_REGION` or AWS configuration. Credentials do not belong in the manifest or a backend configuration file. See [AWS SDK configuration](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-gosdk.html).

For another S3-compatible service, supply its endpoint and signing region:

```json
{
	"backend": "s3",
	"bucket": "zorveus-state",
	"key": "apps/assistant/prod.json",
	"region": "auto",
	"endpoint": "https://account-id.r2.cloudflarestorage.com",
	"usePathStyle": true
}
```

Replace the endpoint with your provider's actual URL. For local MinIO testing, an endpoint such as `http://localhost:9000` is allowed. Other HTTP endpoints are rejected.

The example above uses Cloudflare R2's `auto` region and account endpoint. See [R2 S3 compatibility](https://developers.cloudflare.com/r2/api/s3/api/) and [conditional operations](https://developers.cloudflare.com/r2/api/s3/conditional-operations/).

Your service must implement conditional `PutObject` requests using `If-None-Match` and `If-Match`, and return ETags. These requests protect both state updates and locking. A service that ignores conditional writes is not supported. See [S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html).

The AWS identity needs `s3:GetObject` and `s3:PutObject` on the configured object. The locking protocol does not need object deletion or a separate DynamoDB table. Bucket encryption policies can require additional KMS permissions.

You can put just the backend object in a separate JSON file and pass `--state-config backend.json`. Repeat that flag on commands using the separate file. If the manifest also contains `_state`, the settings must match.

## Initialize or reconnect to state

For a new app without local tracking:

```sh
zorveus state init --config zorveus.prod.json
zorveus plan --config zorveus.prod.json
zorveus apply --plan zorveus.prod.plan.json --yes
```

Initialization creates only the state object. It does not create Zorveus resources. Running `state init` again validates and reuses the existing state, prints `Durable state already initialized`, and succeeds without rewriting its data or releasing a lock. A new checkout can run the same command to restore its local tracking from the backend.

Existing durable state is never overwritten by `state init`. Conflicting resource IDs, organizations, backend locations, or saved state IDs remain errors. If a previously bound state object is missing, initialization stops instead of creating an empty replacement. Storage access failures are also errors.

For an existing app with local tracking, add the backend settings, review the manifest and `.zorveus` state, then copy that tracking explicitly:

```sh
zorveus state init --config zorveus.prod.json --copy-local
```

Finish any local unfinished apply before copying its state. Create a new plan after initialization; earlier local plans are not durable-state plans.

If you reuse a manifest filename for a new app, back up the previous local tracking and initialize fresh state:

```sh
zorveus state init --config zorveus.json --force --local
```

`--force` moves this manifest's local state and apply journals into a private backup directory under `.zorveus` and prints its path. It starts without the previous tracking, including any unfinished local apply. Existing Zorveus resources stay active, and IDs in your manifest stay unchanged. Other manifests' tracking is preserved.

For a fresh start, select an unused S3 object key. If compatible durable state already exists, init reuses it, including when `--force` is supplied. It never resets or overwrites that state. If the S3 creation request fails, the CLI restores the archived local tracking. Use `--copy-local` when continuing the same app instead. The two flags cannot be combined.

## Use another checkout or runner

Keep the desired manifest in your repository. Plan restores missing app and resource IDs in memory by matching resource object names against the last successful apply. It preserves your current settings and proposed removals. Planning does not write those IDs into the input manifest; apply writes them after completing the operation.

Removing a managed inference key from the JSON still proposes revocation on another runner because the shared state retains its previous ID. Omitting an entire resource section preserves it, following the ordinary manifest rules. Destructive changes still require `--allow-delete`.

To apply a reviewed plan artifact from another checkout, provide this runner's manifest path:

```sh
zorveus apply --plan reviewed.plan.json --config zorveus.prod.json --yes --non-interactive
```

Use the same manifest contents used for planning. For a cross-app plan made with `--from`, the source manifest must also be available at the corresponding relative location. State identity, revision, configuration digests, and current backend values are checked before applying.

Cloning a manifest removes its state configuration along with organization and resource IDs. Promotion preserves the target manifest's state configuration. Changing a backend key does not move an existing deployment automatically.

## Recover an interrupted apply

After a completed destroy, `plan` and `apply` can recreate resources using the same manifest and state object. Confirmed-deleted IDs are ignored during planning, and apply saves the replacement IDs. See [Destroy a deployment](destroy.md#after-completion).

Apply checkpoints its intent before sending a mutation and saves returned IDs and progress as the operation proceeds. A new runner can load the pending operation from durable state:

```sh
zorveus state inspect --config zorveus.prod.json
zorveus apply --resume --config zorveus.prod.json --yes --non-interactive
```

Restore the original desired manifest and provider environment values before resuming. Provider inputs are checked using stored fingerprints; their raw values are not uploaded to state. A new plan is blocked while the previous apply remains unfinished.

If a resource creation succeeded but its ID could not be confirmed in durable storage, the CLI stops for reconciliation. It does not repeat an uncertain creation automatically. Durable storage cannot make a Zorveus API request and an S3 write one atomic transaction.

## Inspect and release locks

Plan, apply, and other state-aware operations acquire an exclusive lock. The lock and snapshot occupy the same object, and updates compare the current object version. A stale process cannot overwrite a newer lock or checkpoint.

Healthy commands release their lock when they finish, including ordinary failures. A crashed process or an uncertain storage write can leave a lock behind. Locks do not expire automatically.

Inspect the owner and lock ID:

```sh
zorveus state inspect --config zorveus.prod.json --json
```

After confirming the previous process has stopped and cannot send more writes, release its exact lock:

```sh
zorveus state unlock --config zorveus.prod.json --lock-id exact-lock-id --yes
```

Unlocking does not erase apply progress. Resume the pending operation afterward. Do not unlock a running deployment.

Save a snapshot backup to a new file:

```sh
zorveus state pull --config zorveus.prod.json --out prod-state-backup.json
```

The backup contains state identity, lock information, policies, IDs, and apply progress. The command refuses to overwrite an existing output file.

## Authentication and generated secrets

S3 state contains no management access tokens, refresh tokens, generated client secrets, inference keys, routing-key secrets, or service-key secrets. Those continue to use the CLI's existing OS credential stores. The `.zorveus` directory is a local mirror and is not uploaded as an arbitrary directory archive.

Durable state solves shared IDs, locking, and apply recovery. It does not add unattended management authentication or a CI secret vault. A fresh GitHub-hosted runner still needs those capabilities before it can run a complete deployment. Do not put a browser login or the OS keychain into an Actions cache or state object.

State behavior is covered by simulated S3 request tests and fresh-runner provisioning tests. A live AWS, R2, or MinIO deployment still needs verification against your chosen service and permissions.
