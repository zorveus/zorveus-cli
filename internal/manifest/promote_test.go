package manifest

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestPromotionCopiesConfigurationAndKeepsTargetIdentities(t *testing.T) {
	source, err := Parse([]byte(`{
  "app": {
    "id": "app_dev",
    "name": "Updated assistant",
    "allowedModels": [
      "*"
    ],
    "oauth": {
      "redirectUris": [
        "https://new.example.test/callback"
      ]
    }
  },
  "keys": {
    "backend": {
      "cap": null,
      "id": "dev_backend",
      "name": "Updated backend"
    },
    "worker": {
      "id": "dev_worker",
      "name": "New worker"
    }
  },
  "providerCredentials": {
    "openai": {
      "id": "dev_provider",
      "name": "Updated OpenAI",
      "providerConfig": {
        "base_url": "https://new.example.test"
      },
      "secretFromEnv": "OPENAI_API_KEY"
    }
  },
  "routingKeys": {
    "primary": {
      "defaultSelector": null,
      "id": "dev_router",
      "members": [
        "backend",
        "worker"
      ],
      "name": "Updated router"
    }
  },
  "serviceKeys": {
    "billing": {
      "id": "dev_service",
      "name": "Updated billing",
      "scopes": [
        "product_users:write"
      ]
    }
  },
  "version": 1
}`))
	if err != nil {
		t.Fatal(err)
	}
	target, err := Parse([]byte(`{
  "app": {
    "id": "app_prod",
    "name": "Old assistant",
    "allowedModels": [
      "*"
    ],
    "oauth": {
      "clientType": "confidential",
      "redirectUris": [
        "https://old.example.test/callback"
      ]
    }
  },
  "keys": {
    "backend": {
      "allowedModels": [
        "model_a"
      ],
      "cap": {
        "amount": "10",
        "currency": "USD",
        "period": "monthly"
      },
      "id": "prod_backend",
      "name": "Old backend"
    },
    "retired": {
      "id": "prod_retired",
      "name": "Retired"
    }
  },
  "providerCredentials": {
    "openai": {
      "id": "prod_provider",
      "provider": "openai",
      "providerConfig": {
        "api_base": "https://old.example.test"
      },
      "routingMode": "auto_resolve"
    }
  },
  "routingKeys": {
    "primary": {
      "defaultSelector": "backend",
      "id": "prod_router",
      "members": [
        "backend"
      ],
      "name": "Old router"
    }
  },
  "serviceKeys": {
    "billing": {
      "id": "prod_service",
      "name": "Old billing",
      "scopes": [
        "product_users:read"
      ]
    }
  },
  "version": 1
}`))
	if err != nil {
		t.Fatal(err)
	}
	sourceBefore, _ := source.Bytes()
	targetBefore, _ := target.Bytes()
	promoted, err := Promote(source, target)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		App                 map[string]any            `json:"app"`
		Keys                map[string]map[string]any `json:"keys"`
		RoutingKeys         map[string]map[string]any `json:"routingKeys"`
		ServiceKeys         map[string]map[string]any `json:"serviceKeys"`
		ProviderCredentials map[string]map[string]any `json:"providerCredentials"`
	}
	if err := json.Unmarshal(promoted, &result); err != nil {
		t.Fatal(err)
	}
	if result.App["id"] != "app_prod" || result.App["name"] != "Updated assistant" {
		t.Fatal("app configuration did not update under the target identity")
	}
	oauth := result.App["oauth"].(map[string]any)
	if oauth["clientType"] != "confidential" || oauth["redirectUris"].([]any)[0] != "https://new.example.test/callback" {
		t.Fatal("nested fields did not follow normal omitted-field semantics")
	}
	if len(result.Keys) != 2 || result.Keys["worker"]["id"] != nil || result.Keys["backend"]["id"] != "prod_backend" {
		t.Fatal("resource matching used source IDs or array order instead of object names")
	}
	if result.Keys["backend"]["name"] != "Updated backend" {
		t.Fatal("matched key did not receive source settings")
	}
	if cap, exists := result.Keys["backend"]["cap"]; !exists || cap != nil {
		t.Fatal("explicit null did not clear the cap")
	}
	if _, exists := result.Keys["backend"]["allowedModels"]; exists {
		t.Fatal("source model inheritance did not replace the target's explicit key policy")
	}
	if result.RoutingKeys["primary"]["id"] != "prod_router" || result.RoutingKeys["primary"]["name"] != "Updated router" || result.RoutingKeys["primary"]["defaultSelector"] != nil {
		t.Fatal("router settings or target ID were lost")
	}
	if result.ServiceKeys["billing"]["id"] != "prod_service" || result.ServiceKeys["billing"]["scopes"].([]any)[0] != "product_users:write" {
		t.Fatal("service-key scopes or target ID were lost")
	}
	if result.ProviderCredentials["openai"]["id"] != "prod_provider" || result.ProviderCredentials["openai"]["secretFromEnv"] != "OPENAI_API_KEY" || result.ProviderCredentials["openai"]["routingMode"] != "auto_resolve" {
		t.Fatal("provider settings or target ID were lost")
	}
	config := result.ProviderCredentials["openai"]["providerConfig"].(map[string]any)
	if len(config) != 1 || config["base_url"] != "https://new.example.test" {
		t.Fatal("providerConfig did not replace the whole configuration object")
	}
	if bytes.Contains(promoted, []byte("dev_")) || bytes.Contains(promoted, []byte("app_dev")) || bytes.Contains(promoted, []byte("prod_retired")) {
		t.Fatal("source IDs or removed target entries survived promotion")
	}
	sourceAfter, _ := source.Bytes()
	targetAfter, _ := target.Bytes()
	if !bytes.Equal(sourceBefore, sourceAfter) || !bytes.Equal(targetBefore, targetAfter) {
		t.Fatal("promotion mutated an input document")
	}
}

func TestPromotionPreservesOmittedSectionsAndHonorsEmptyObjects(t *testing.T) {
	target, err := Parse([]byte(`{
  "app": {
    "id": "app_prod",
    "name": "Production",
    "allowedModels": [
      "*"
    ]
  },
  "keys": {
    "backend": {
      "id": "prod_key"
    }
  },
  "routingKeys": {
    "public": {
      "id": "prod_router",
      "members": [
        "backend"
      ]
    }
  },
  "version": 1
}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		source string
		keeps  bool
	}{
		{"omitted", `{"version":1,"app":{"id":"app_dev","creditUnitsPerUsd":"1000.000000000001"}}`, true},
		{"empty", `{
  "app": {
    "id": "app_dev"
  },
  "keys": {},
  "routingKeys": {},
  "version": 1
}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, err := Parse([]byte(test.source))
			if err != nil {
				t.Fatal(err)
			}
			promoted, err := Promote(source, target)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(promoted, []byte("prod_key")) != test.keeps || bytes.Contains(promoted, []byte("prod_router")) != test.keeps {
				t.Fatal("omitted and empty sections lost their distinct meaning")
			}
		})
	}
}

func TestPromotionRequiresCreationFieldsForUnmatchedResources(t *testing.T) {
	source, err := Parse([]byte(`{"version":1,"app":{"id":"app_dev"},"keys":{"worker":{"id":"dev_worker"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	target, err := Parse([]byte(`{"version":1,"app":{"id":"app_prod"}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Promote(source, target)
	assertProblem(t, err, "required_field", "/keys/worker/name")
}
