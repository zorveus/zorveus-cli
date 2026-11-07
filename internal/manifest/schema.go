package manifest

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

//go:embed schema/v1.json
var schemaSource []byte

var compiledSchema = sync.OnceValues(compileSchema)

func compileSchema() (*jsonschema.Schema, error) {
	const id = "urn:zorveus:manifest:1"
	definition, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaSource))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource(id, definition); err != nil {
		return nil, err
	}
	return compiler.Compile(id)
}

func validateSchema(value map[string]any) error {
	number, _ := value["version"].(json.Number)
	version, _ := number.Float64()
	if version != 1 {
		return invalid("unsupported_version", "Only manifest version 1 is supported.", "/version")
	}
	if err := validateResourceNames(value); err != nil {
		return err
	}
	schema, err := compiledSchema()
	if err != nil {
		return problem.New("schema_unavailable", "The embedded manifest schema could not be loaded.", 1)
	}
	err = schema.Validate(value)
	if err == nil {
		return nil
	}
	var validation *jsonschema.ValidationError
	if !errors.As(err, &validation) {
		return invalid("invalid_manifest", "The manifest does not match its version's schema.", "")
	}
	issues := schemaIssues(validation)
	slices.SortFunc(issues, func(left, right *problem.Error) int {
		return strings.Compare(*left.Pointer+left.Code, *right.Pointer+right.Code)
	})
	return issues[0]
}

func schemaIssues(validation *jsonschema.ValidationError) []*problem.Error {
	pointer := ""
	for _, field := range validation.InstanceLocation {
		pointer = childPointer(pointer, field)
	}
	if len(validation.Causes) > 0 {
		var issues []*problem.Error
		for _, cause := range validation.Causes {
			issues = append(issues, schemaIssues(cause)...)
		}
		return issues
	}
	if pointer == "/version" {
		return []*problem.Error{invalid("unsupported_version", "Only manifest version 1 is supported.", pointer)}
	}
	switch issue := validation.ErrorKind.(type) {
	case *kind.Required:
		var issues []*problem.Error
		for _, field := range issue.Missing {
			issues = append(issues, invalid("required_field", "This field is required.", childPointer(pointer, field)))
		}
		return issues
	case *kind.AdditionalProperties:
		var issues []*problem.Error
		for _, field := range issue.Properties {
			issues = append(issues, invalid("unknown_field", "This field is not supported in this manifest version.", childPointer(pointer, field)))
		}
		return issues
	case *kind.Type:
		return []*problem.Error{invalid("invalid_type", "This field has the wrong JSON type. Check the manifest schema.", pointer)}
	case *kind.Pattern:
		return []*problem.Error{invalid("invalid_format", "This field does not match its required format. Check the manifest schema.", pointer)}
	case *kind.Enum:
		return []*problem.Error{invalid("invalid_choice", "This field is not one of the supported values. Check the manifest schema.", pointer)}
	default:
		return []*problem.Error{invalid("schema_validation_failed", "This field does not satisfy the manifest schema.", pointer)}
	}
}
