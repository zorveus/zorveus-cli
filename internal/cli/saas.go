package cli

import (
	"strings"

	"github.com/zorveus/zorveus-cli/internal/manifest"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

func saasOptions(name string, models []string, credits string, plans []string) (manifest.SaaSOptions, error) {
	if len(models) == 0 {
		models = []string{"*"}
	}
	for _, model := range models {
		if strings.TrimSpace(model) == "" {
			return manifest.SaaSOptions{}, problem.New("model_required", "Provide a model ID or * to allow all models.", 2)
		}
	}
	options := manifest.SaaSOptions{Name: name, Models: models, CreditsPerUSD: credits}
	if len(plans) == 0 {
		options.Plans = []manifest.SaaSPlan{{Reference: "plus", Name: "Plus", Allowance: "10"}}
	}
	for _, value := range plans {
		reference, rest, found := strings.Cut(value, "=")
		separator := strings.LastIndex(rest, ":")
		if !found || separator < 0 {
			return options, problem.New("invalid_plan_input", "Use --plan 'reference=Display name:monthly USD allowance', for example --plan 'plus=Plus:10'. Repeat it for more plans.", 2)
		}
		options.Plans = append(options.Plans, manifest.SaaSPlan{Reference: reference, Name: rest[:separator], Allowance: rest[separator+1:]})
	}
	_, err := manifest.SaaS(options)
	return options, err
}
