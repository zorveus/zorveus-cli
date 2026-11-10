# Set up customer allowance plans

Use `zorveus setup` to configure an app, set its model permissions, define customer allowance plans, review the proposed resources, and apply them. Setup ends after provisioning completes.

You need a Zorveus account and an available OS credential store. Your organization needs access to your chosen model and enough funding for inference. Setup creates configuration and credentials. It does not purchase credits or send an inference request.

## Run the wizard

Run this command from your application's directory:

```sh
zorveus setup
```

For a local Zorveus backend, use `zorveus setup --local`.

Setup checks your saved login. If you need to sign in, it opens browser consent, where you select the organization. It displays the API endpoint and organization before preparing resources.

If your session expires while you answer questions or apply changes, setup explains what happened and waits for Enter before opening sign-in again. After sign-in, setup continues with your saved answers and progress. Use the same organization and user. Press Ctrl+C to cancel without sending the pending API request.

The wizard asks for:

1. Your app's display name.
2. Model permissions. Press Enter to use `"*"`, which allows all models. Enter model IDs separated by commas to restrict access instead. The wizard does not discover or verify available models.
3. Credit units per USD. The default is `100`, shared by every plan.
4. A plan display name and a monthly AI allowance in USD per customer.

The first plan defaults to `Plus`, reference `plus`, with a `10` USD allowance. At `100` credit units per USD, that allowance equals `1,000` credits per customer each month. Answer yes to **Add another plan?** to configure more plans in the same session. Press Enter to continue with your current plans.

Questions use cyan, completed steps use green, and warnings use amber. Defaults and hints use muted text. Colors follow your terminal's palette. Use `--no-color` or set `NO_COLOR=1` to disable them. Redirected output, `TERM=dumb`, and JSON output contain no color escape sequences.

The wizard generates each plan's JSON key from its display name, such as `Plus` to `plus`. It asks for a JSON key only if the generated key is already used or the name cannot be converted. Use lowercase letters, digits, underscores, or hyphens. Keep this key stable when editing the display name later. It identifies the inference key in your manifest; it does not require or create a routing key.

An AI allowance is different from your subscription price. Your application and payment provider manage subscriptions, payments, and customer plan assignments. The selected inference key supplies the AI policy. Switching keys does not promise a reset of the customer's existing usage.

## Review and apply

After entering your plans, choose **Save and review plan?** to save `zorveus.json` and prepare the ordinary saved plan. The Review section shows each changed resource and field without per-resource braces or repeated command instructions. For a new setup, the confirmation asks **Create these resources in your Zorveus account?**. Plans containing updates or removals ask **Apply these changes to your Zorveus account?**. Approval performs the reviewed changes in the selected organization. The standalone `zorveus plan` command retains its full Terraform-style display.

For a new app, the plan includes:

- One app permitting all models by default, or your specified model, with the chosen credit conversion rate.
- One direct inference connection per plan, with its monthly allowance, `creditMode: "enforce"`, and `overrunPolicy: "limit_output"`.
- One shared organization service key under `serviceKeys.customers`, with `product_users:read` for customer reads.

Enforcement and output limiting follow the backend's existing behavior. The wizard does not add routing keys, provider credentials, or payment-provider configuration.

Apply creates the resources, writes their IDs into your local manifest, and saves generated secrets in the OS credential store. Secrets stay out of manifests, plans, journals, and setup output. Use `retrieve` to print one saved value explicitly.

When setup reports **Setup complete**, your resources are ready. Run your application through your usual development or deployment workflow.

If you cancel before saving, setup leaves no manifest. If you decline apply after reviewing the plan, the complete manifest and saved plan remain available; no resource changes are applied. Setup prints the exact `zorveus apply --plan ... --yes` command to run later, including `--allow-delete` when the plan requires it. If apply is interrupted, rerun setup to review and resume the original saved plan. An uncertain operation can still require explicit recovery through the existing [apply recovery workflow](../README.md#resume-an-interrupted-apply).

## Run setup again

Setup displays the existing file, app name and ID, model permissions, credit conversion, and plan allowances. It then asks **Use this configuration?**. Answer yes to review its plan. An unchanged configuration completes without another apply question.

Answer no to choose **Rewrite this configuration?**. Answer yes to edit the app settings and each plan through the wizard, with current values as defaults. Existing app and matched resource IDs stay intact. Changing a plan's display name keeps its JSON key. You can add more plans in the same session.

Rewrite preserves settings outside the wizard, including OAuth, routing, provider credentials, and existing organization service keys. The current file is backed up under `.zorveus/setup-backup-*` only after you approve saving the rewrite. You then review and approve the remote changes separately. Canceling either question keeps the existing file. Creation flags such as `--name` and `--plan` do not silently overwrite existing files; non-interactive updates can be made by editing the manifest and running plan/apply.

Setup checks local tracking before asking for app or plan details. If it conflicts with your configuration, setup looks for applied configurations in the same folder and selected organization and API. It identifies them by their saved app IDs.

Choose your existing app to restore its configuration and resource IDs to `zorveus.json`. Setup backs up the previous default file and tracking under `.zorveus/setup-backup-*` first. The existing app is reused. When its settings already match, setup completes without another apply question.

To create a different app, explicitly choose **Start a new app**. Setup keeps the current filename and saved answers, and preserves conflicting tracking in `.zorveus/*.state.backup-*.json`. Existing backend apps and keys stay active. A new app is created only after you approve the plan. Canceling recovery keeps the current files intact.

Non-interactive setup stops on a conflict. Resolve it interactively or select the intended configuration with `--config`. An unfinished apply must be resumed before its tracking can be reset. Completed apply journals are retained so old creation plans cannot run again accidentally.

## Retrieve saved secrets

After applying your configuration, retrieve a secret by its manifest reference:

```sh
zorveus retrieve keys.plus
```

The command prints only that secret, followed by a newline. It does not require a model or another key.

Choose the item you need:

| Item | Saved secret |
| --- | --- |
| `app` | App OAuth client secret, for a confidential app |
| `keys.plus` | Inference key for the `plus` entry |
| `routingKeys.primary` | Routing key for the `primary` entry |
| `serviceKeys.customers` | Organization service key for the `customers` entry |

The named entry must exist in your manifest. To use another file or select an item by its ID:

```sh
zorveus retrieve keys.plus --config zorveus.dev.json
zorveus retrieve aconn_example --config zorveus.dev.json
```

Replace `aconn_example` with the `id` from the intended manifest entry. An ID must belong to an item in the selected file.

On macOS, you can copy a secret directly to the clipboard:

```sh
zorveus retrieve keys.plus | pbcopy
```

For scripts, `--json` returns the selected `item`, `id`, and `secret` inside the standard result envelope:

```sh
zorveus retrieve serviceKeys.customers --config zorveus.dev.json --json --non-interactive
```

This command deliberately outputs the secret. Keep that output in your application's server configuration or secret manager. `retrieve` is a CLI command and is not exposed through MCP.

Retrieval reads the secret saved in your OS credential store. It checks your login, resource access, and available key metadata first. It does not create resources, rotate keys, or write secrets into the manifest.

Provider credentials come from the environment variable named by `secretFromEnv`. The CLI does not save those values for later retrieval. Public apps do not have a client secret.

## Keep saved secrets available

The CLI stores credentials by API origin, organization, CLI user, resource kind, and resource ID. Display names, manifest references, and later plan filenames do not determine which credential is retrieved.

Renaming an entry or applying an unchanged configuration does not lose its saved credential. Retrieval checks inference-key IDs, routing-key status, and service-key prefixes against backend metadata. An inference key or service key that has expired, been revoked, or been rotated externally cannot use its old saved value. App metadata does not include a client-secret version, so retrieval cannot detect an app secret rotated outside this CLI.

Credentials created by older CLI versions can be registered when their completed apply journal and original resource reference remain available. If the originally applied manifest is unchanged, rerunning its original saved apply plan also registers the saved credentials. This registration does not rotate them.

An imported manifest contains IDs, not secret values. Moving a manifest to another computer does not copy the OS credential store. If a secret is unavailable, restore its credential-store entry or explicitly recover or rotate the resource through the supported workflow. Retrieval reports the missing value instead of creating a replacement.

## Use explicit inputs

For automation, provide the configuration without prompts:

```sh
zorveus setup --non-interactive \
  --name "Writing app" \
  --credits-per-usd 100 \
  --plan 'plus=Plus:10' \
  --plan 'premium=Premium:30'
```

This saves a manifest and plan without applying them. Add `--yes` to approve apply. You must already be logged in; non-interactive setup cannot open browser consent. Missing required inputs produce an error before creating a manifest. Omit `--plan` to use the default Plus plan. Omit `--model` to allow all models, or pass `--model YOUR_MODEL_ID` to restrict access. With `--config`, the saved plan follows the manifest name, such as `zorveus.dev.plan.json`.

Each `--plan` has the form `reference=Display name:monthly USD allowance`. The allowance and credit conversion are positive decimal strings. Calculations preserve decimal precision.

To create only the manifest, with no login or API requests:

```sh
zorveus init --starter saas \
  --name "Writing app" \
  --plan 'plus=Plus:10' \
  --plan 'premium=Premium:30'
```

Guided setup and offline initialization use the same manifest builder. Offline initialization refuses to overwrite an existing file. Ordinary `zorveus init` retains its existing behavior.
