package manifest

import (
	"math/big"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

func (document *Document) validate() error {
	if err := validateIdentities(document.data); err != nil {
		return err
	}
	if err := validateDecimals(document.data, ""); err != nil {
		return err
	}
	if err := validateConsent(document.view.App.Consent); err != nil {
		return err
	}
	if err := validateRedirectURIs(document.data); err != nil {
		return err
	}
	if err := validateProviderSettings(document.data); err != nil {
		return err
	}
	if err := document.validateModels(); err != nil {
		return err
	}
	return document.validateRouting()
}

func validateProviderSettings(data map[string]any) error {
	for _, entry := range resourceEntries(data, "providerCredentials") {
		credential := entry.Fields
		config, _ := credential["providerConfig"].(map[string]any)
		pointer := entry.Pointer + "/providerConfig"
		_, configProvided := credential["providerConfig"]
		provider, _ := credential["provider"].(string)
		if credential["id"] == nil || (configProvided && provider != "") {
			if err := ValidateProviderConfig(provider, config, pointer); err != nil {
				return err
			}
		}
		for _, name := range []string{"api_base", "base_url"} {
			value, exists := config[name].(string)
			if !exists {
				continue
			}
			parsed, err := url.Parse(value)
			if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return invalid("invalid_provider_url", "Use an HTTPS provider URL without embedded credentials, query parameters, or fragments.", pointer+"/"+name)
			}
		}
	}
	return nil
}

// ValidateProviderConfig also checks updates after planning resolves the provider.
func ValidateProviderConfig(provider string, config map[string]any, pointer string) error {
	required := []string{}
	if provider == "azure" {
		required = []string{"api_base", "api_version"}
	}
	if provider == "vertex_ai" {
		required = []string{"project", "location"}
	}
	for _, name := range required {
		value, _ := config[name].(string)
		if strings.TrimSpace(value) == "" {
			return invalid("provider_config_required", "This provider requires a nonempty providerConfig field.", pointer+"/"+name)
		}
	}
	return nil
}

func validateIdentities(data map[string]any) error {
	app := data["app"].(map[string]any)
	ids := make(map[string]bool)
	if id, exists := app["id"].(string); exists {
		ids[id] = true
	}
	for _, section := range []string{"keys", "routingKeys", "serviceKeys", "providerCredentials"} {
		for _, resource := range resourceEntries(data, section) {
			id, exists := resource.Fields["id"].(string)
			if !exists {
				continue
			}
			if (section == "keys" || section == "routingKeys") && app["id"] == nil {
				return invalid("app_identity_required", "Existing app-owned resources require the existing app's ID.", resource.Pointer+"/id")
			}
			if ids[id] {
				return invalid("duplicate_id", "A resource ID appears more than once in the manifest.", resource.Pointer+"/id")
			}
			ids[id] = true
		}
	}
	return nil
}

func (document *Document) validateModels() error {
	appModels := document.view.App.AllowedModels
	for _, key := range document.view.Keys {
		pointer := key.Pointer + "/allowedModels"
		if appModels == nil {
			document.deferCheck("remote_models_required", pointer, "Planning must read the app's current model permissions before resolving this key's policy.")
			continue
		}
		if slices.Contains(appModels, "*") {
			continue
		}
		for modelIndex, model := range key.AllowedModels {
			if !slices.Contains(appModels, model) {
				return invalid("model_not_allowed", "A key's model permissions must be a subset of the app's model permissions.", childPointer(pointer, strconv.Itoa(modelIndex)))
			}
		}
	}
	return nil
}

func (document *Document) validateRouting() error {
	aliases := make(map[string]bool)
	for _, key := range document.view.Keys {
		aliases[key.RoutingAlias] = true
	}
	for _, router := range document.view.RoutingKeys {
		pointer := router.Pointer
		if router.DefaultSelector != nil {
			if router.Members == nil {
				document.deferCheck("remote_members_required", pointer+"/defaultSelector", "Planning must check this selector against the router's existing members.")
			} else if !slices.Contains(router.Members, *router.DefaultSelector) {
				return invalid("invalid_default_selector", "The defaultSelector must identify one of this router's members.", pointer+"/defaultSelector")
			}
		}
		for memberIndex, alias := range router.Members {
			if aliases[alias] {
				continue
			}
			location := pointer + "/members/" + strconv.Itoa(memberIndex)
			if document.view.Keys == nil && document.view.App.ID != "" {
				document.deferCheck("remote_alias_required", location, "The keys section is omitted. Planning must resolve this alias from the CLI's saved configuration.")
				continue
			}
			return invalid("unknown_member", "A routing member must reference a declared inference key.", location)
		}
	}
	return nil
}

func (document *Document) deferCheck(code, pointer, message string) {
	document.checks = append(document.checks, Check{Code: code, Pointer: pointer, Message: message})
}

func validateDecimals(value any, pointer string) error {
	switch value := value.(type) {
	case map[string]any:
		fields := make([]string, 0, len(value))
		for field := range value {
			fields = append(fields, field)
		}
		slices.Sort(fields)
		for _, field := range fields {
			location := childPointer(pointer, field)
			if amount, ok := value[field].(string); ok && isDecimalField(field) {
				if err := validateDecimal(amount, field, location); err != nil {
					return err
				}
			}
			if err := validateDecimals(value[field], location); err != nil {
				return err
			}
		}
	case []any:
		for index, item := range value {
			if err := validateDecimals(item, childPointer(pointer, strconv.Itoa(index))); err != nil {
				return err
			}
		}
	}
	return nil
}

func isDecimalField(field string) bool {
	switch field {
	case "amount", "minimumCapAmount", "maximumCapAmount", "creditUnitsPerUsd", "productUserChargeMultiplier":
		return true
	default:
		return false
	}
}

func validateDecimal(amount, field, pointer string) error {
	value, ok := new(big.Rat).SetString(amount)
	if !ok || value.Sign() <= 0 {
		return invalid("invalid_decimal", "This amount must be a positive finite decimal string. Zero is not supported.", pointer)
	}
	decimalPlaces := 12
	if field == "productUserChargeMultiplier" {
		decimalPlaces = 6
		if value.Cmp(big.NewRat(1, 1)) < 0 {
			return invalid("invalid_decimal", "A charge multiplier must be at least 1.", pointer)
		}
	}
	whole, fraction, _ := strings.Cut(amount, ".")
	whole = strings.TrimLeft(whole, "0")
	fraction = strings.TrimRight(fraction, "0")
	if len(whole) > 12 || len(fraction) > decimalPlaces {
		return invalid("decimal_out_of_bounds", "This decimal exceeds the backend's precision or size bounds. Use at most 12 whole digits and 12 fractional digits, or 6 fractional digits for a charge multiplier.", pointer)
	}
	return nil
}

func validateConsent(consent *consentView) error {
	if consent == nil {
		return nil
	}
	minimum := decimalValue(consent.MinimumCapAmount)
	maximum := decimalValue(consent.MaximumCapAmount)
	if minimum != nil && maximum != nil && minimum.Cmp(maximum) > 0 {
		return invalid("invalid_cap_policy", "The maximum cap must be at least the minimum cap.", "/app/consent/maximumCapAmount")
	}
	if consent.DefaultCap == nil {
		return nil
	}
	amount, _ := new(big.Rat).SetString(consent.DefaultCap.Amount)
	if (minimum != nil && amount.Cmp(minimum) < 0) || (maximum != nil && amount.Cmp(maximum) > 0) {
		return invalid("invalid_cap_policy", "The default cap must fall within the configured minimum and maximum.", "/app/consent/defaultCap/amount")
	}
	return nil
}

func decimalValue(value *string) *big.Rat {
	if value == nil {
		return nil
	}
	amount, _ := new(big.Rat).SetString(*value)
	return amount
}

func validateRedirectURIs(data map[string]any) error {
	app := data["app"].(map[string]any)
	oauth, _ := app["oauth"].(map[string]any)
	redirects, _ := oauth["redirectUris"].([]any)
	for index, value := range redirects {
		redirect, err := url.Parse(value.(string))
		if err != nil || redirect.Hostname() == "" || redirect.User != nil || redirect.Fragment != "" {
			return invalid("invalid_redirect_uri", "Redirect URIs must be absolute HTTP or HTTPS URLs without embedded credentials or fragments.", "/app/oauth/redirectUris/"+strconv.Itoa(index))
		}
	}
	return nil
}
