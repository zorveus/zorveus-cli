package manifest

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

const maxFileSize = 4 << 20

type Document struct {
	data   map[string]any
	view   manifestView
	checks []Check
}

type Check struct {
	Code    string `json:"code"`
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

type ValidationResult struct {
	Config         string  `json:"config"`
	Version        int     `json:"version"`
	Valid          bool    `json:"valid"`
	DeferredChecks []Check `json:"deferred_checks"`
}

type manifestView struct {
	App         appView      `json:"app"`
	Keys        []keyView    `json:"keys"`
	RoutingKeys []routerView `json:"routingKeys"`
}

type appView struct {
	ID            string       `json:"id"`
	AllowedModels []string     `json:"allowedModels"`
	Consent       *consentView `json:"consent"`
}

type keyView struct {
	Pointer       string   `json:"-"`
	ID            string   `json:"id"`
	RoutingAlias  string   `json:"-"`
	AllowedModels []string `json:"allowedModels"`
}

type routerView struct {
	Pointer         string   `json:"-"`
	ID              string   `json:"id"`
	Members         []string `json:"members"`
	DefaultSelector *string  `json:"defaultSelector"`
}

type consentView struct {
	DefaultCap       *capView `json:"defaultCap"`
	MinimumCapAmount *string  `json:"minimumCapAmount"`
	MaximumCapAmount *string  `json:"maximumCapAmount"`
}

type capView struct {
	Amount string `json:"amount"`
}

func Load(path string) (*Document, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, problem.New("config_read_failed", "Could not open the manifest. Check --config or --from and the file permissions.", 1)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return nil, problem.New("config_read_failed", "Could not read the manifest.", 1)
	}
	return Parse(data)
}

func Parse(data []byte) (*Document, error) {
	if len(data) > maxFileSize {
		return nil, invalid("manifest_too_large", "The manifest must be at most 4 MiB.", "")
	}
	if !utf8.Valid(data) {
		return nil, invalid("invalid_json", "The manifest must contain valid UTF-8 JSON.", "")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readValue(decoder, "", 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, invalid("invalid_json", "The manifest must contain exactly one JSON object.", "")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, invalid("invalid_manifest", "The manifest must be a JSON object.", "")
	}
	if err := validateSchema(object); err != nil {
		return nil, err
	}
	document := &Document{data: object, checks: []Check{}}
	if err := document.readView(); err != nil {
		return nil, invalid("invalid_manifest", "The manifest could not be read.", "")
	}
	if err := document.validate(); err != nil {
		return nil, err
	}
	return document, nil
}

func (document *Document) Result(path string) ValidationResult {
	return ValidationResult{Config: path, Version: document.Version(), Valid: true, DeferredChecks: document.checks}
}

func (document *Document) Bytes() ([]byte, error) {
	return encodeAndValidate(document.data)
}

func readValue(decoder *json.Decoder, pointer string, depth int) (any, error) {
	if depth > 64 {
		return nil, invalid("json_too_deep", "JSON nesting must not exceed 64 levels.", pointer)
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, invalid("invalid_json", "The manifest contains invalid JSON.", pointer)
	}
	delimiter, isContainer := token.(json.Delim)
	if !isContainer {
		return token, nil
	}
	if delimiter == '{' {
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, invalid("invalid_json", "Could not read a JSON object key.", pointer)
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, invalid("invalid_json", "JSON object keys must be strings.", pointer)
			}
			location := childPointer(pointer, key)
			if _, exists := object[key]; exists {
				return nil, invalid("duplicate_json_key", "A JSON object key is repeated. Keep each field only once.", location)
			}
			value, err := readValue(decoder, location, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
			return nil, invalid("invalid_json", "The JSON object is not closed correctly.", pointer)
		}
		return object, nil
	}
	if delimiter == '[' {
		items := []any{}
		for decoder.More() {
			value, err := readValue(decoder, childPointer(pointer, strconv.Itoa(len(items))), depth+1)
			if err != nil {
				return nil, err
			}
			items = append(items, value)
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
			return nil, invalid("invalid_json", "The JSON array is not closed correctly.", pointer)
		}
		return items, nil
	}
	return nil, invalid("invalid_json", "Unexpected JSON delimiter.", pointer)
}

func childPointer(parent, field string) string {
	escaped := strings.ReplaceAll(strings.ReplaceAll(field, "~", "~0"), "/", "~1")
	return parent + "/" + escaped
}

func invalid(code, message, pointer string) *problem.Error {
	err := problem.New(code, message, 2)
	err.Pointer = &pointer
	return err
}
