package provision

import (
	"encoding/json"
	"math/big"
	"slices"

	"github.com/zorveus/zorveus-cli/internal/api"
)

var consentFields = []string{"default_cap_amount", "default_cap_currency", "default_cap_period", "minimum_cap_amount", "maximum_cap_amount", "allow_unlimited_cap"}

func appProfile(app Fields) Fields {
	payload := Fields{}
	for source, destination := range map[string]string{
		"name": "app_name", "creditUnitsPerUsd": "credit_units_per_usd",
		"productUserChargeMultiplier": "product_user_charge_multiplier",
	} {
		if value, exists := app[source]; exists {
			payload[destination] = value
		}
	}
	var consent Fields
	_ = json.Unmarshal(app["consent"], &consent)
	for source, destination := range map[string]string{
		"minimumCapAmount": "minimum_cap_amount", "maximumCapAmount": "maximum_cap_amount",
		"allowUnlimitedCap": "allow_unlimited_cap",
	} {
		if value, exists := consent[source]; exists {
			payload[destination] = value
		}
	}
	if consent.has("defaultCap") {
		var cap Fields
		_ = json.Unmarshal(consent["defaultCap"], &cap)
		for source, destination := range map[string]string{"amount": "default_cap_amount", "currency": "default_cap_currency", "period": "default_cap_period"} {
			payload[destination] = cap.pick(source)[source]
		}
	}
	return payload
}

// Validate partial consent changes against the bounds that will remain remote.
func mergeConsent(payload, current Fields) error {
	hasConsent := slices.ContainsFunc(consentFields, payload.has)
	if !hasConsent {
		return nil
	}
	for _, name := range consentFields {
		if !payload.has(name) {
			payload[name] = current.pick(name)[name]
		}
	}
	minimum := decimalField(payload, "minimum_cap_amount")
	maximum := decimalField(payload, "maximum_cap_amount")
	amount := decimalField(payload, "default_cap_amount")
	if minimum != nil && maximum != nil && minimum.Cmp(maximum) > 0 {
		return manifestError("invalid_cap_policy", "The resulting maximum cap must be at least the minimum cap.", "/app/consent/maximumCapAmount")
	}
	if amount != nil && ((minimum != nil && amount.Cmp(minimum) < 0) || (maximum != nil && amount.Cmp(maximum) > 0)) {
		return manifestError("invalid_cap_policy", "The resulting default cap must fall within the minimum and maximum.", "/app/consent/defaultCap/amount")
	}
	return nil
}

func decimalField(value Fields, name string) *big.Rat {
	number, _ := new(big.Rat).SetString(value.text(name))
	return number
}

func (plan *Plan) planRedirects(appID string, current api.ManagedApp, desired Fields) {
	if !desired.has("redirectUris") {
		return
	}
	wanted := desired.strings("redirectUris")
	existing := appFields(current).strings("redirect_uris")
	// Add first so replacing a callback does not leave the app without one.
	for _, uri := range wanted {
		if !slices.Contains(existing, uri) {
			plan.Steps = append(plan.Steps, Step{Kind: "app_redirect_add", Ref: "app", ID: appID,
				Before: fields(map[string]any{"present": false, "redirect_uri": uri}), After: fields(map[string]any{"present": true, "redirect_uri": uri})})
		}
	}
	for _, redirect := range current.RedirectURIs {
		if redirect.Status != "active" || slices.Contains(wanted, redirect.URI) {
			continue
		}
		plan.Steps = append(plan.Steps, Step{Kind: "app_redirect_remove", Ref: "app", ID: redirect.ID,
			Before: fields(map[string]any{"present": true, "redirect_uri": redirect.URI}), After: fields(map[string]any{"present": false, "redirect_uri": redirect.URI}), Destructive: true})
	}
	// A guard also catches redirects added by someone else after planning.
	plan.add("app_redirects", "app", appID, appFields(current), fields(map[string]any{"redirect_uris": wanted}))
}

func stepHasCredential(step Step) bool {
	return step.Kind == "create_key" || step.Kind == "create_router" || step.Kind == "create_service" || (step.Kind == "create_app" && step.After.text("client_type") != "public")
}

func hasAppBilling(payload Fields) bool {
	return payload.has("credit_units_per_usd") || payload.has("product_user_charge_multiplier") || slices.ContainsFunc(consentFields, payload.has)
}
