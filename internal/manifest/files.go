package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

type FileResult struct {
	Config  string `json:"config"`
	Version int    `json:"version"`
}

func Init(path, name string, models []string) (FileResult, error) {
	data := map[string]any{
		"version": 1,
		"app":     map[string]any{"name": name, "allowedModels": models},
	}
	encoded, err := encodeAndValidate(data)
	if err != nil {
		return FileResult{}, err
	}
	if err := writeNewFile(path, encoded); err != nil {
		return FileResult{}, err
	}
	return FileResult{Config: path, Version: 1}, nil
}

func Clone(source, destination, name string) (FileResult, error) {
	document, err := Load(source)
	if err != nil {
		return FileResult{}, err
	}
	for _, section := range []string{"serviceKeys", "providerCredentials"} {
		resources, _ := document.data[section].([]any)
		if len(resources) > 0 {
			return FileResult{}, invalid("shared_clone_policy_required", "Cloning shared organization credentials requires a reuse or replacement policy, which is not implemented yet. Prepare a separate manifest without those sections.", "/"+section)
		}
	}
	app := document.data["app"].(map[string]any)
	delete(app, "id")
	if name != "" {
		app["name"] = name
	}
	for _, section := range []string{"keys", "routingKeys"} {
		resources, _ := document.data[section].([]any)
		for _, resource := range resources {
			delete(resource.(map[string]any), "id")
		}
	}
	encoded, err := encodeAndValidate(document.data)
	if err != nil {
		return FileResult{}, err
	}
	if err := writeNewFile(destination, encoded); err != nil {
		return FileResult{}, err
	}
	return FileResult{Config: destination, Version: 1}, nil
}

func encodeAndValidate(data map[string]any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(data); err != nil {
		return nil, problem.New("config_encode_failed", "Could not encode the manifest.", 1)
	}
	encoded := buffer.Bytes()
	if _, err := Parse(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func writeNewFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return problem.New("destination_exists", "The destination already exists. Choose another file; existing files are never overwritten.", 1)
	}
	if err != nil {
		return problem.New("config_write_failed", "Could not create the manifest. Check the destination directory and permissions.", 1)
	}
	_, writeError := file.Write(data)
	closeError := file.Close()
	if writeError != nil || closeError != nil {
		_ = os.Remove(path)
		return problem.New("config_write_failed", "Could not finish writing the manifest.", 1)
	}
	return nil
}
