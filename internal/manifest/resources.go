package manifest

import (
	"encoding/json"
	"regexp"
	"slices"
)

var resourceName = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

type Resource struct {
	Name    string
	Pointer string
	Fields  map[string]any
}

func (document *Document) AppID() string {
	return document.view.App.ID
}

func (document *Document) Version() int {
	version, _ := document.data["version"].(json.Number)
	value, _ := version.Float64()
	return int(value)
}

// Resources returns named entries in a deterministic execution order.
func (document *Document) Resources(section string) []Resource {
	return resourceEntries(document.data, section)
}

func resourceEntries(data map[string]any, section string) []Resource {
	entries, _ := data[section].(map[string]any)
	names := sortedResourceNames(entries)
	resources := make([]Resource, 0, len(names))
	for _, name := range names {
		resources = append(resources, Resource{Name: name, Pointer: childPointer("/"+section, name), Fields: entries[name].(map[string]any)})
	}
	return resources
}

func sortedResourceNames(entries map[string]any) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func validateResourceNames(data map[string]any) error {
	for _, section := range []string{"keys", "routingKeys", "serviceKeys", "providerCredentials"} {
		entries, _ := data[section].(map[string]any)
		for _, name := range sortedResourceNames(entries) {
			if !resourceName.MatchString(name) {
				return invalid("invalid_format", "Resource names must use 1 to 64 lowercase letters, digits, underscores, or hyphens.", childPointer("/"+section, name))
			}
		}
	}
	return nil
}

func (document *Document) readView() error {
	app, _ := json.Marshal(document.data["app"])
	if err := json.Unmarshal(app, &document.view.App); err != nil {
		return err
	}
	if _, exists := document.data["keys"]; exists {
		document.view.Keys = []keyView{}
	}
	for _, entry := range document.Resources("keys") {
		var key keyView
		data, _ := json.Marshal(entry.Fields)
		if err := json.Unmarshal(data, &key); err != nil {
			return err
		}
		key.Pointer = entry.Pointer
		key.RoutingAlias = entry.Name
		document.view.Keys = append(document.view.Keys, key)
	}
	for _, entry := range document.Resources("routingKeys") {
		var router routerView
		data, _ := json.Marshal(entry.Fields)
		if err := json.Unmarshal(data, &router); err != nil {
			return err
		}
		router.Pointer = entry.Pointer
		document.view.RoutingKeys = append(document.view.RoutingKeys, router)
	}
	return nil
}
