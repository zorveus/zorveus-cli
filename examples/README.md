# Example manifests

Choose a use case below and copy its complete JSON into `zorveus.json` in a new project directory. Edit the settings for your app, then follow [Try an example](#try-an-example). Each manifest contains no resource IDs or secret values.

## Use-case index

Select a use case to jump to its complete JSON and explanation. The separate JSON files contain the same configuration for copying or downloading.

| Use case | Manifest | What it creates |
| --- | --- | --- |
| [Minimal app](#minimal-app) | [minimal.json](minimal.json) | An app that allows all models |
| [Internal tool](#internal-tool) | [internal-tool.json](internal-tool.json) | An app and an inference key with a monthly USD 100 cap |
| [Subscription tiers](#subscription-tiers) | [subscriptions.json](subscriptions.json) | Two inference keys with different caps and one router requiring an explicit tier selector |
| [Business credits](#business-credits) | [business-credits.json](business-credits.json) | An app conversion rate, charge multipliers, two capped keys, and two routers |
| [OAuth and consent](#oauth-and-consent) | [oauth-wallet.json](oauth-wallet.json) | A public OAuth app with a callback URL and consent cap defaults |
| [OpenAI BYOK](#openai-byok) | [byok-openai.json](byok-openai.json) | An app, a capped inference key, and an organization provider credential |
| [Service key](#service-key) | [service-key-backend.json](service-key-backend.json) | An app and an organization service key with product-user read/write permissions |

For the commands shared by every example, see [Try an example](#try-an-example). For model lists, amounts, and routing aliases, see [Customize the configuration](#customize-the-configuration).

To carry changes into an existing production app, see [Promote an example to production](#promote-an-example-to-production).

## Try an example

Start with `internal-tool.json` if you want an inference key. Start with `minimal.json` if you only want to test creating and updating an app.

After saving your chosen example in your project, run:

```sh
zorveus validate
zorveus login
zorveus plan
```

Validation works offline. Login chooses the organization. Plan checks remote permissions and resource settings, then saves `zorveus.plan.json` for review.

When the plan matches what you want:

```sh
zorveus apply --plan zorveus.plan.json --yes
zorveus status
zorveus plan
```

Apply writes the created resource IDs into your copy of the manifest. Keep those IDs when making updates. The last plan should report no changes. Applying another fresh copy without IDs requests new resources.

Generated secrets are saved in your OS credential store; apply reports their account names. See [review and apply changes](../README.md#review-and-apply-changes) for retrieval and recovery details.

If you are working from the CLI repository, copy an example into your project before applying it. Apply updates the manifest, so applying directly inside `examples/` would change the checked-in starter file.

## Customize the configuration

Most examples use `allowedModels: ["*"]`. Replace it with specific model identifiers to restrict access. Inference keys inherit the app's model list unless they declare their own. Planning checks model availability.

Amounts, conversion rates, and multipliers are decimal strings. For example, use `"100"`, not `100`. A cap limits spending; applying it does not buy credits or fund a wallet.

An inference key can work directly. A router is useful when one credential needs to select between multiple keys. Router `members` refer to the object names in `keys`; the CLI resolves those aliases to backend resource IDs.

## Promote an example to production

Keep the development and production manifests returned by apply. Resource object names identify matching roles in the two files. To bring development settings into production:

```sh
zorveus login
zorveus plan --from zorveus.dev.json --config zorveus.prod.json --keep-names
zorveus apply --plan zorveus.prod.plan.json --yes
```

Select the production organization during login. Plan leaves both files unchanged. Apply updates the production JSON with source policies and settings while keeping existing production IDs and display names. New entries receive new production IDs; later plans keep them.

`keys.backend` matches `keys.backend`, and `routingKeys.primary` matches `routingKeys.primary`. Keep those object names stable across environments. The `--keep-names` flag preserves existing production display names. Omit it to copy source names too. With the flag, new entries use the source name unless their target entry already has a name. Removing a managed key or router from an explicit object can propose revocation, requiring `--allow-delete` during apply.

See the [cross-app planning guide](../docs/promotion.md) for complete examples and credential rules.

## Minimal app

Use [minimal.json](minimal.json) to try the app lifecycle before adding credentials or spending policies. It creates one app named `My first app` and allows all models with `allowedModels: ["*"]`.

```json
{
  "version": 1,
  "app": {
    "name": "My first app",
    "allowedModels": [
      "*"
    ]
  }
}
```

Change `app.name` before your first plan. After applying, change the name again and run plan to see an update to the same app. Keep the `app.id` that apply added to your file.

This example creates no inference key, router, service key, or provider credential. To call inference from your backend, start with [Internal tool](#internal-tool) or add its `keys` section to your saved manifest.

## Internal tool

Use [internal-tool.json](internal-tool.json) when a server-side tool needs one inference credential with a spending limit. It creates an app and a Backend key with a monthly USD 100 cap.

```json
{
  "version": 1,
  "app": {
    "name": "Internal assistant",
    "allowedModels": [
      "*"
    ]
  },
  "keys": {
    "backend": {
      "name": "Backend",
      "creditMode": "enforce_if_present",
      "overrunPolicy": "limit_output",
      "cap": {
        "amount": "100",
        "currency": "USD",
        "period": "monthly"
      }
    }
  }
}
```

`internal-tool.json` uses `enforce_if_present`: customer credit enforcement applies when a product user is supplied. It does not require every request to have a product user. The connection's monthly cap still applies.

There is no router in this example. Use the generated inference key directly from your backend.

Change `cap.amount` to your budget and `cap.period` to `daily`, `monthly`, or `lifetime`. Keep the generated credential on your server.

## Subscription tiers

Use [subscriptions.json](subscriptions.json) when your application has Free and Pro customers and needs a separate inference policy for each tier.

```json
{
  "version": 1,
  "app": {
    "name": "Subscription assistant",
    "allowedModels": [
      "*"
    ]
  },
  "keys": {
    "free": {
      "name": "Free plan",
      "creditMode": "enforce",
      "overrunPolicy": "limit_output",
      "cap": {
        "amount": "1",
        "currency": "USD",
        "period": "monthly"
      }
    },
    "pro": {
      "name": "Pro plan",
      "creditMode": "enforce",
      "overrunPolicy": "limit_output",
      "cap": {
        "amount": "10",
        "currency": "USD",
        "period": "monthly"
      }
    }
  },
  "routingKeys": {
    "primary": {
      "name": "Subscription backend",
      "members": [
        "free",
        "pro"
      ],
      "defaultSelector": null
    }
  }
}
```

`subscriptions.json` gives Free and Pro keys monthly USD 1 and USD 10 caps. Both use `enforce` for customer credit enforcement.

The router has `defaultSelector: null`, so requests must select `free` or `pro`. Your trusted backend selects the alias from verified subscription data. Keep the router credential on that backend.

Your application still handles subscription purchases, customer identity, and tier assignments at runtime. Applying this file does not create subscriptions or collect payments.

Change each key's cap to your tier's allowance. To give the tiers different model access, add an `allowedModels` list to each key. Keep each list within the models allowed by the app.

## Business credits

Use [business-credits.json](business-credits.json) when your product presents customer allowances as business credits and needs different charge policies across tiers.

```json
{
  "version": 1,
  "app": {
    "name": "Business assistant",
    "allowedModels": [
      "*"
    ],
    "creditUnitsPerUsd": "1000",
    "productUserChargeMultiplier": "2"
  },
  "keys": {
    "starter": {
      "name": "Starter plan",
      "creditMode": "enforce",
      "overrunPolicy": "limit_output",
      "cap": {
        "amount": "5",
        "currency": "USD",
        "period": "monthly"
      }
    },
    "business": {
      "name": "Business plan",
      "creditMode": "enforce",
      "overrunPolicy": "carry_overrun_forward",
      "productUserChargeMultiplier": "1.5",
      "cap": {
        "amount": "25",
        "currency": "USD",
        "period": "monthly"
      }
    }
  },
  "routingKeys": {
    "primary": {
      "name": "Primary backend",
      "members": [
        "starter",
        "business"
      ],
      "defaultSelector": null
    },
    "business": {
      "name": "Business integration",
      "members": [
        "business"
      ],
      "defaultSelector": "business"
    }
  }
}
```

`business-credits.json` sets 1,000 business credits per USD. Starter inherits the app's 2x customer allowance charge multiplier; Business overrides it with 1.5x. These multipliers affect customer allowance consumption without multiplying the organization's wallet charge.

The primary router requires an explicit tier. The second router defaults to `business`. Both routers use the same Business connection and share its billing state; the second router does not grant another allowance.

Business uses `carry_overrun_forward` for completed usage that exceeds its allowance. Review that policy before adopting it. Purchases, grants, customer assignments, and replenishment remain runtime operations in your backend.

Change `creditUnitsPerUsd` to your product's credit conversion rate. Set the app multiplier as the default, then use a key's `productUserChargeMultiplier` only when that tier needs an override.

## OAuth and consent

Use [oauth-wallet.json](oauth-wallet.json) when users authorize your application through OAuth and choose a spending cap during consent. It configures the app registration and consent defaults without creating a programmatic inference key.

```json
{
  "version": 1,
  "app": {
    "name": "Connected assistant",
    "allowedModels": [
      "*"
    ],
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
```

Before using `oauth-wallet.json`, replace `https://example.com/oauth/callback` with your application's callback URL. It declares a public client; use `confidential` when your server can securely hold a client secret.

The example sets a monthly USD 10 consent cap default, with amounts between USD 1 and USD 50, and disallows unlimited caps. User authorization and connections happen later through your application's OAuth flow.

Choose `oauth.clientType` and `allowedScopes` before creating the app. The CLI can set these during creation and check existing values, but cannot change them on an existing app through the current app update API.

## OpenAI BYOK

Use [byok-openai.json](byok-openai.json) when you want eligible inference requests to use your own OpenAI provider account. BYOK means bring your own key.

```json
{
  "version": 1,
  "app": {
    "name": "OpenAI document processor",
    "allowedModels": [
      "openai/gpt-4o-mini"
    ]
  },
  "keys": {
    "processor": {
      "name": "Processing backend",
      "creditMode": "enforce_if_present",
      "overrunPolicy": "limit_output",
      "cap": {
        "amount": "50",
        "currency": "USD",
        "period": "monthly"
      }
    }
  },
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

Set `OPENAI_API_KEY` in the environment of the shell running the CLI before planning and applying `byok-openai.json`. The CLI does not automatically load `.env` files. Keep the actual key out of the JSON file.

The example restricts the app and credential policy to `openai/gpt-4o-mini`. Change both lists if you choose another model available to your organization and provider account.

The credential belongs to the organization. With `auto_resolve`, other eligible connections in that organization can use it according to its model policy. It is not exclusive to this app. Applying the file stores the credential but does not send an inference request.

## Service key

Use [service-key-backend.json](service-key-backend.json) when your backend needs a machine credential to manage product users without an interactive user login.

```json
{
  "version": 1,
  "app": {
    "name": "Customer assistant",
    "allowedModels": [
      "*"
    ]
  },
  "serviceKeys": {
    "customers": {
      "name": "Customer management backend",
      "scopes": [
        "product_users:read",
        "product_users:write"
      ]
    }
  }
}
```

`service-key-backend.json` creates a machine credential for product-user read/write APIs. It does not create an inference key; add a `keys` section if the same backend also needs inference access.

The service key belongs to the organization and requires organization-wide management consent. Keep its secret on your server. Listing it in this app's manifest does not limit its access to that app.

Service keys and provider credentials are shared organization resources. Omitting them from a later manifest does not revoke them. Manifests containing these resources cannot currently be used with `zorveus clone`.

See the [manifest reference](../docs/manifest-v1.md) for all fields, update rules, and validation limits, or the [CLI README](../README.md) for installation, importing existing apps, and command usage.
