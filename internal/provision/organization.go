package provision

import (
	"fmt"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

// Older manifests keep their organization in local state instead of the JSON.
func ProjectTarget(config, origin, organization string) (Target, error) {
	var state State
	exists, _, err := loadJSON(statePath(config), &state)
	if err != nil {
		return Target{}, err
	}
	if exists {
		if state.Version != 1 || state.Target.Org == "" || state.Target.Origin == "" {
			return Target{}, problem.New("state_target_mismatch", "The saved CLI state has no valid organization or API origin. Check this project's .zorveus state before continuing.", 4)
		}
		if organization != "" && organization != state.Target.Org {
			message := fmt.Sprintf("The manifest specifies organization %q, but its saved state belongs to organization %q. Use the matching manifest and organization, or a separate manifest.", organization, state.Target.Org)
			return Target{}, problem.New("state_target_mismatch", message, 4)
		}
		if origin != "" && origin != state.Target.Origin {
			message := fmt.Sprintf("The command selected API server %q, but this manifest's saved state uses %q. Use the saved API server or a separate manifest.", origin, state.Target.Origin)
			return Target{}, problem.New("state_target_mismatch", message, 4)
		}
		organization = state.Target.Org
		origin = state.Target.Origin
	}
	return Target{Origin: origin, Org: organization}, nil
}
