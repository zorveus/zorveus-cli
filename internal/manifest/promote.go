package manifest

import "encoding/json"

// Promote copies configuration, while resource identities belong to the target.
func Promote(source, target *Document) ([]byte, error) {
	// Work on a copy so a rejected merge cannot change either input document.
	targetBytes, err := target.Bytes()
	if err != nil {
		return nil, err
	}
	var promoted map[string]any
	if err := json.Unmarshal(targetBytes, &promoted); err != nil {
		return nil, err
	}
	sourceApp := source.data["app"].(map[string]any)
	targetApp := promoted["app"].(map[string]any)
	if targetApp["id"] == nil {
		return nil, invalid("promotion_target_required", "Promotion requires an existing target app ID. Use clone to prepare a new app, or import the existing target app first.", "/app/id")
	}
	mergePromotionFields(sourceApp, targetApp)
	for _, section := range []string{"keys", "routingKeys", "serviceKeys", "providerCredentials"} {
		entries, supplied := source.data[section].(map[string]any)
		if !supplied {
			continue
		}
		existing, _ := promoted[section].(map[string]any)
		promoted[section] = promoteNamedResources(section, entries, existing)
	}
	return encodeAndValidate(promoted)
}

func promoteNamedResources(section string, source, target map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for name, value := range source {
		entry := value.(map[string]any)
		matched, _ := target[name].(map[string]any)
		if matched == nil {
			matched = map[string]any{}
		}
		mergePromotionFields(entry, matched)
		if _, explicitModels := entry["allowedModels"]; section == "keys" && !explicitModels {
			delete(matched, "allowedModels")
		}
		result[name] = matched
	}
	return result
}

func mergePromotionFields(source, target map[string]any) {
	for field, value := range source {
		if field == "id" {
			continue
		}
		// OAuth and consent support partial updates; providerConfig replaces its object.
		if sourceObject, ok := value.(map[string]any); ok && (field == "oauth" || field == "consent") {
			targetObject, _ := target[field].(map[string]any)
			if targetObject == nil {
				targetObject = map[string]any{}
			}
			mergePromotionFields(sourceObject, targetObject)
			target[field] = targetObject
			continue
		}
		target[field] = value
	}
}
