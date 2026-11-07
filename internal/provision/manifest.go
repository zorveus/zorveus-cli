package provision

import (
	"encoding/json"

	"github.com/zorveus/zorveus-cli/internal/manifest"
)

// The execution engine uses ordered entries. Named JSON resources retain their names
// separately, so references and ID writeback never depend on object iteration order.
func (desired *Manifest) UnmarshalJSON(data []byte) error {
	document, err := manifest.Parse(data)
	if err != nil {
		return err
	}
	var raw Fields
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*desired = Manifest{Version: document.Version(), resourceNames: map[string][]string{}}
	if err := json.Unmarshal(raw["app"], &desired.App); err != nil {
		return err
	}
	for _, section := range []string{"keys", "routingKeys", "serviceKeys", "providerCredentials"} {
		if !raw.has(section) {
			continue
		}
		entries := document.Resources(section)
		resources := make([]Fields, 0, len(entries))
		for _, entry := range entries {
			resource := fields(entry.Fields)
			if section == "keys" {
				resource.set("routingAlias", entry.Name)
			}
			resources = append(resources, resource)
			desired.resourceNames[section] = append(desired.resourceNames[section], entry.Name)
		}
		switch section {
		case "keys":
			desired.Keys = resources
		case "routingKeys":
			desired.Routers = resources
		case "serviceKeys":
			desired.ServiceKeys = resources
		case "providerCredentials":
			desired.Providers = resources
		}
	}
	return nil
}

func resourceKind(section string) string {
	switch section {
	case "keys":
		return "key"
	case "routingKeys":
		return "router"
	case "serviceKeys":
		return "service"
	default:
		return "provider"
	}
}

func (desired Manifest) resourceRef(section string, index int) string {
	name := desired.resourceNames[section][index]
	return resourceKind(section) + ":" + name
}

func (desired Manifest) resourcePointer(section string, index int) string {
	name := desired.resourceNames[section][index]
	return "/" + section + "/" + name
}

func (desired Manifest) resourceValue(section string, resources []Fields) map[string]Fields {
	result := make(map[string]Fields, len(resources))
	for index, resource := range resources {
		entry := Fields{}
		for field, value := range resource {
			if field != "routingAlias" {
				entry[field] = value
			}
		}
		result[desired.resourceNames[section][index]] = entry
	}
	return result
}
