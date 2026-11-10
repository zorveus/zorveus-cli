package manifest

// RewriteSaaS changes wizard-owned settings while retaining existing identities
// and settings the wizard does not ask about.
func RewriteSaaS(original *Document, options SaaSOptions) ([]byte, error) {
	data, err := original.Bytes()
	if err != nil {
		return nil, err
	}
	target, err := Parse(data)
	if err != nil {
		return nil, err
	}
	data, err = SaaS(options)
	if err != nil {
		return nil, err
	}
	source, err := Parse(data)
	if err != nil {
		return nil, err
	}
	mergePromotionFields(source.data["app"].(map[string]any), target.data["app"].(map[string]any), false)
	existingKeys, _ := target.data["keys"].(map[string]any)
	keys := map[string]any{}
	for name, value := range source.data["keys"].(map[string]any) {
		key, _ := existingKeys[name].(map[string]any)
		if key == nil {
			key = map[string]any{}
		}
		mergePromotionFields(value.(map[string]any), key, false)
		keys[name] = key
	}
	target.data["keys"] = keys
	services, _ := target.data["serviceKeys"].(map[string]any)
	if services == nil {
		services = map[string]any{}
	}
	if _, exists := services["customers"]; !exists {
		services["customers"] = source.data["serviceKeys"].(map[string]any)["customers"]
	}
	target.data["serviceKeys"] = services
	return encodeAndValidate(target.data)
}
