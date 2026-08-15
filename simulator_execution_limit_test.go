package libcommand

import (
	"context"
	"strings"
	"testing"
)

func TestSimulatorRecursionUsesExecutionStepLimit(t *testing.T) {
	simulator := NewBuilder().
		Limits(&Limits{MaxExecutionSteps: 150}).
		Build()
	err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `f() { f; }; f`})
	if err == nil || !strings.Contains(err.Error(), "maximum execution step count 150 reached") {
		t.Fatalf("Simulate() error = %v", err)
	}
}
