package manifest

import (
	"math/big"
	"strings"
	"unicode"
)

type SaaSPlan struct {
	Reference string
	Name      string
	Allowance string
}

type SaaSRouting struct {
	Name            string
	DefaultSelector string
}

type SaaSOptions struct {
	Name          string
	Models        []string
	CreditsPerUSD string
	Plans         []SaaSPlan
	Routing       *SaaSRouting
}

// SaaS builds the same manifest for guided and non-interactive setup.
func SaaS(options SaaSOptions) ([]byte, error) {
	if strings.TrimSpace(options.Name) == "" {
		return nil, invalid("app_name_required", "Provide an app name.", "/app/name")
	}
	if len(options.Models) == 0 {
		options.Models = []string{"*"}
	}
	for _, model := range options.Models {
		if model == "" || strings.TrimSpace(model) != model {
			return nil, invalid("model_required", "Choose model IDs or * to allow all models.", "/app/allowedModels")
		}
	}
	if len(options.Plans) == 0 {
		return nil, invalid("plan_required", "Configure at least one customer plan.", "/keys")
	}
	keys := map[string]any{}
	for _, plan := range options.Plans {
		if _, exists := keys[plan.Reference]; exists {
			return nil, invalid("duplicate_plan_reference", "Each plan needs a different reference. Display names can be edited independently.", "/keys/"+plan.Reference)
		}
		if strings.TrimSpace(plan.Name) == "" {
			return nil, invalid("plan_name_required", "Provide a display name for every plan.", "/keys/"+plan.Reference+"/name")
		}
		keys[plan.Reference] = map[string]any{
			"name": plan.Name, "creditMode": "enforce", "overrunPolicy": "limit_output",
			"cap": map[string]string{"amount": plan.Allowance, "currency": "USD", "period": "monthly"},
		}
	}
	document := map[string]any{
		"version":     1,
		"app":         map[string]any{"name": options.Name, "allowedModels": options.Models, "creditUnitsPerUsd": options.CreditsPerUSD},
		"keys":        keys,
		"serviceKeys": map[string]any{"customers": map[string]any{"name": "Customer reads", "scopes": []string{"product_users:read"}}},
	}
	if options.Routing != nil {
		members := make([]string, 0, len(options.Plans))
		for _, plan := range options.Plans {
			members = append(members, plan.Reference)
		}
		name := options.Routing.Name
		if name == "" {
			name = options.Name + " router"
		}
		selector := options.Routing.DefaultSelector
		if selector == "" {
			selector = members[0]
		}
		document["routingKeys"] = map[string]any{"primary": map[string]any{
			"name": name, "members": members, "defaultSelector": selector,
		}}
	}
	return encodeAndValidate(document)
}

func InitSaaS(path string, options SaaSOptions) (FileResult, error) {
	data, err := SaaS(options)
	if err != nil {
		return FileResult{}, err
	}
	if err := writeNewFile(path, data); err != nil {
		return FileResult{}, err
	}
	return FileResult{Config: path, Version: 1}, nil
}

// PlanReference suggests a local name. The caller still checks for collisions.
func PlanReference(name string) string {
	var result strings.Builder
	for _, letter := range strings.ToLower(name) {
		if letter >= 'a' && letter <= 'z' || letter >= '0' && letter <= '9' {
			result.WriteRune(letter)
			continue
		}
		if unicode.IsSpace(letter) || letter == '-' || letter == '_' {
			result.WriteByte('-')
		}
	}
	return strings.Trim(result.String(), "-")
}

func CreditAllowance(amount, rate string) string {
	allowance, ok := new(big.Rat).SetString(amount)
	if !ok {
		return ""
	}
	conversion, ok := new(big.Rat).SetString(rate)
	if !ok {
		return ""
	}
	// Two accepted manifest decimals can produce up to 24 fractional places.
	value := new(big.Rat).Mul(allowance, conversion).FloatString(24)
	return strings.TrimSuffix(strings.TrimRight(value, "0"), ".")
}
