package provision

import (
	"encoding/json"
	"math/big"
	"reflect"
	"slices"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

type Fields map[string]json.RawMessage

func fields(value any) Fields {
	data, _ := json.Marshal(value)
	var result Fields
	_ = json.Unmarshal(data, &result)
	return result
}

func (f Fields) has(name string) bool { _, ok := f[name]; return ok }
func (f Fields) text(name string) string {
	var value string
	_ = json.Unmarshal(f[name], &value)
	return value
}
func (f Fields) strings(name string) []string {
	var value []string
	_ = json.Unmarshal(f[name], &value)
	return value
}
func (f Fields) set(name string, value any) { f[name], _ = json.Marshal(value) }
func (f Fields) pick(names ...string) Fields {
	result := Fields{}
	for _, name := range names {
		if value, ok := f[name]; ok {
			result[name] = value
		} else {
			result[name] = json.RawMessage("null")
		}
	}
	return result
}

func (f Fields) only(names ...string) Fields {
	result := Fields{}
	for _, name := range names {
		if value, exists := f[name]; exists {
			result[name] = value
		}
	}
	return result
}

type Manifest struct {
	App         Fields   `json:"app"`
	Keys        []Fields `json:"keys"`
	Routers     []Fields `json:"routingKeys"`
	ServiceKeys []Fields `json:"serviceKeys"`
	Providers   []Fields `json:"providerCredentials"`
}

type Member struct {
	Ref   string `json:"ref"`
	Alias string `json:"alias"`
}

type Step struct {
	Kind        string   `json:"kind"`
	Ref         string   `json:"ref"`
	ID          string   `json:"id,omitempty"`
	Before      Fields   `json:"before"`
	After       Fields   `json:"after"`
	Members     []Member `json:"members,omitempty"`
	CapID       string   `json:"cap_id,omitempty"`
	Destructive bool     `json:"destructive,omitempty"`
	SecretEnv   string   `json:"secret_from_env,omitempty"`
	SecretKind  string   `json:"secret_kind,omitempty"`
	VersionID   string   `json:"expected_secret_version,omitempty"`
}

// FieldChanges uses the same comparisons as planning, including exact decimal values.
func (step Step) FieldChanges() []FieldChange {
	names := map[string]bool{}
	for name := range step.Before {
		names[name] = true
	}
	for name := range step.After {
		names[name] = true
	}
	changes := []FieldChange{}
	for name := range names {
		if sameFields(step.Before.pick(name), step.After.pick(name)) {
			continue
		}
		changes = append(changes, FieldChange{Field: name, Actual: step.Before[name], Desired: step.After[name]})
	}
	slices.SortFunc(changes, func(left, right FieldChange) int { return strings.Compare(left.Field, right.Field) })
	return changes
}

type Target struct {
	Origin string `json:"api_origin"`
	Org    string `json:"organization"`
	User   string `json:"user_id"`
}

type Plan struct {
	Version      int               `json:"version"`
	ID           string            `json:"id"`
	Target       Target            `json:"target"`
	Config       string            `json:"config"`
	ConfigDigest string            `json:"config_digest"`
	StateDigest  string            `json:"state_digest"`
	Manifest     json.RawMessage   `json:"manifest"`
	Steps        []Step            `json:"steps"`
	Scopes       []string          `json:"required_scopes"`
	Bindings     map[string]string `json:"bindings"`
}

type State struct {
	Version int               `json:"version"`
	Target  Target            `json:"target"`
	AppID   string            `json:"app_id"`
	Keys    map[string]string `json:"keys"`
	Routers []string          `json:"routing_keys"`
}

type Progress struct {
	ID               string `json:"resource_id,omitempty"`
	CredentialID     string `json:"credential_id,omitempty"`
	Started          bool   `json:"started,omitempty"`
	Done             bool   `json:"done,omitempty"`
	SecretPrefix     string `json:"secret_prefix,omitempty"`
	RotationStarted  bool   `json:"rotation_started,omitempty"`
	RotationReceived bool   `json:"rotation_received,omitempty"`
}

type Journal struct {
	Config      string     `json:"config"`
	Version     int        `json:"version"`
	PlanDigest  string     `json:"plan_digest"`
	Steps       []Progress `json:"steps"`
	FinalDigest string     `json:"final_config_digest,omitempty"`
	Complete    bool       `json:"complete"`
}

type PlanResult struct {
	Plan           string   `json:"plan"`
	Operations     int      `json:"operations"`
	Changes        []Step   `json:"changes"`
	RequiredScopes []string `json:"required_scopes"`
}

type ApplyResult struct {
	Operation          string   `json:"operation"`
	Config             string   `json:"config"`
	AppID              string   `json:"app_id"`
	Completed          int      `json:"completed_steps"`
	CredentialAccounts []string `json:"credential_accounts"`
}

func sameFields(left, right Fields) bool {
	if len(left) != len(right) {
		return false
	}
	for name, value := range left {
		other, ok := right[name]
		if !ok {
			return false
		}
		if !reflect.DeepEqual(normalize(name, value), normalize(name, other)) {
			return false
		}
	}
	return true
}

func normalize(name string, raw json.RawMessage) any {
	var value any
	_ = json.Unmarshal(raw, &value)
	if name == "app_name" || name == "key_alias" || name == "credential_name" {
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
	}
	if name == "provider_config" {
		var config map[string]string
		_ = json.Unmarshal(raw, &config)
		if len(config) == 0 {
			return nil
		}
		return config
	}
	if name == "cap" && value != nil {
		var cap Fields
		_ = json.Unmarshal(raw, &cap)
		result := map[string]any{}
		for field, value := range cap {
			result[field] = normalize(field, value)
		}
		return result
	}
	if name == "allowed_models" || name == "approved_scopes" || name == "allowed_scopes" || name == "scopes" || name == "redirect_uris" || name == "default_model_policy" {
		var items []string
		_ = json.Unmarshal(raw, &items)
		slices.Sort(items)
		return items
	}
	if name == "amount" || name == "credit_units_per_usd" || name == "product_user_charge_multiplier" || name == "default_cap_amount" || name == "minimum_cap_amount" || name == "maximum_cap_amount" {
		if text, ok := value.(string); ok {
			if number, valid := new(big.Rat).SetString(text); valid {
				return number.RatString()
			}
		}
	}
	return value
}

func conflict() error {
	return problem.New("stale_plan", "The manifest, local state, or a planned remote value changed. Create a new plan before applying.", 4)
}
func incomplete() error {
	return problem.New("apply_incomplete", "Apply stopped with an uncertain outcome. Keep the saved plan and local apply journal. Resolve the recorded resource before retrying; creation will not be repeated.", 5)
}
