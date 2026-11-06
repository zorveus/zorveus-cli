package provision

import (
	"context"
	"strings"
	"testing"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

func TestApplyWaitsForCapVisibilityWithoutRepeatingTheWrite(t *testing.T) {
	service, backend, _, config, path := setup(t)
	backend.capReadDelay = 1
	planAndApply(t, service, config, path)
	capWrites := 0
	for _, write := range backend.writes {
		if strings.HasSuffix(write, "/cap") {
			capWrites++
		}
	}
	if capWrites != 1 {
		t.Fatalf("a cap mutation was repeated: %v", backend.writes)
	}
}

func TestReadBackRejectsAnUnexpectedValueAndHasABoundedWait(t *testing.T) {
	for _, scenario := range []string{"conflict", "never_visible"} {
		t.Run(scenario, func(t *testing.T) {
			service, backend, _, config, path := setup(t)
			if scenario == "conflict" {
				backend.capReadBackAmount = "9.99"
			} else {
				backend.capReadDelay = 100
			}
			if _, err := service.Plan(context.Background(), "", config, path); err != nil {
				t.Fatal(err)
			}
			_, err := service.Apply(context.Background(), "", path, true, false)
			if err == nil || problem.Safe(err).Code != "stale_plan" {
				t.Fatalf("unexpected read-back result: %v", err)
			}
			capWrites := 0
			for _, write := range backend.writes {
				if strings.HasSuffix(write, "/cap") {
					capWrites++
				}
			}
			if capWrites != 1 {
				t.Fatal("failed confirmation repeated a mutation")
			}
		})
	}
}
