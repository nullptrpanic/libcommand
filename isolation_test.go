package libcommand

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestHostFileIsolation(t *testing.T) {
	directory := t.TempDir()
	hostFile := filepath.Join(directory, "host.txt")
	if err := os.WriteFile(hostFile, []byte("host"), 0o600); err != nil {
		t.Fatal(err)
	}

	simulator := mustBuildSimulator(t, "inspect", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		if len(invocation.Args) != 1 || argumentString(t, invocation.Args[0]) != "virtual" {
			t.Fatalf("invocation = %#v", invocation)
		}
		return &CommandResult{}, nil
	})
	source := `echo virtual > host.txt; inspect "$(<host.txt)"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(hostFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "host" {
		t.Fatalf("host file changed to %q", content)
	}
}

func TestSimulatorConcurrentReuse(t *testing.T) {
	const simulations = 32
	seen := make(map[string]int, simulations)
	var mutex sync.Mutex
	simulator := mustBuildSimulator(t, "record", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		mutex.Lock()
		seen[argumentString(t, invocation.Args[0])]++
		mutex.Unlock()
		return &CommandResult{}, nil
	})

	var wait sync.WaitGroup
	errorsByIndex := make([]error, simulations)
	for index := range simulations {
		wait.Add(1)
		go func() {
			defer wait.Done()
			requestID := fmt.Sprintf("request-%d", index)
			errorsByIndex[index] = simulator.Simulate(context.Background(), &SimulationRequest{
				Source: `record "$REQUEST"`,
				Env:    map[string]string{"REQUEST": requestID},
			})
		}()
	}
	wait.Wait()
	for index, err := range errorsByIndex {
		if err != nil {
			t.Fatalf("simulation %d: %v", index, err)
		}
		requestID := fmt.Sprintf("request-%d", index)
		if seen[requestID] != 1 {
			t.Fatalf("%s callbacks = %d", requestID, seen[requestID])
		}
	}
}

func TestConfiguredExecutionLimitIsPerSimulation(t *testing.T) {
	const simulations = 32
	var callbacks atomic.Int64
	simulator := NewBuilder().
		Limits(&Limits{MaxExecutionSteps: 1}).
		Command("record", func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
			callbacks.Add(1)
			return &CommandResult{}, nil
		}).
		Build()

	var wait sync.WaitGroup
	errorsByIndex := make([]error, simulations)
	for index := range simulations {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsByIndex[index] = simulator.Simulate(context.Background(), &SimulationRequest{Source: `record`})
		}()
	}
	wait.Wait()
	for index, err := range errorsByIndex {
		if err != nil {
			t.Fatalf("simulation %d: %v", index, err)
		}
	}
	if got := callbacks.Load(); got != simulations {
		t.Fatalf("callbacks = %d, want %d", got, simulations)
	}
}
