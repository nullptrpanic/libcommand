package libcommand

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBuilderStoresDefaultLimits(t *testing.T) {
	for _, builder := range []*Builder{
		NewBuilder(),
		NewBuilder().Limits(&Limits{}),
	} {
		if builder.limits.MaxExecutionSteps != defaultMaxExecutionSteps {
			t.Fatalf("builder limit = %d, want %d", builder.limits.MaxExecutionSteps, defaultMaxExecutionSteps)
		}
		simulator := builder.Build()
		if simulator.limits.MaxExecutionSteps != defaultMaxExecutionSteps {
			t.Fatalf("simulator limit = %d, want %d", simulator.limits.MaxExecutionSteps, defaultMaxExecutionSteps)
		}
		if builder.limits.MaxMemoryBytes != defaultMaxMemoryBytes {
			t.Fatalf("builder memory limit = %d, want %d", builder.limits.MaxMemoryBytes, defaultMaxMemoryBytes)
		}
		if simulator.limits.MaxMemoryBytes != defaultMaxMemoryBytes {
			t.Fatalf("simulator memory limit = %d, want %d", simulator.limits.MaxMemoryBytes, defaultMaxMemoryBytes)
		}
	}
}

func TestZeroValueBuilderAcceptsCommandRegistration(t *testing.T) {
	callbacks := 0
	simulator := new(Builder).
		Command("record", countInvocations(&callbacks)).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `record`}); err != nil {
		t.Fatal(err)
	}
	if callbacks != 1 {
		t.Fatalf("callbacks = %d, want 1", callbacks)
	}
}

func TestSimulatorLimitsRequestMaterialization(t *testing.T) {
	const maximum = 1024
	const source = `record </dev/null`
	callbacks := 0
	simulator := NewBuilder().
		Limits(&Limits{MaxMemoryBytes: maximum}).
		Command("record", countInvocations(&callbacks)).
		Build()

	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: source,
		Stdin:  []byte(strings.Repeat("x", maximum-len(source))),
	}); err != nil {
		t.Fatalf("exact boundary: %v", err)
	}
	if callbacks != 1 {
		t.Fatalf("callbacks = %d, want 1", callbacks)
	}

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: source,
		Stdin:  []byte(strings.Repeat("x", maximum-len(source)+1)),
	})
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 1024 reached") {
		t.Fatalf("one byte over boundary: %v", err)
	}
	if callbacks != 1 {
		t.Fatalf("oversized request invoked handler: callbacks = %d", callbacks)
	}
}

func TestSimulatorUsesConfiguredMemoryLimitDuringEvaluation(t *testing.T) {
	simulator := NewBuilder().
		Limits(&Limits{MaxMemoryBytes: 128}).
		Build()
	for _, source := range []string{
		`printf -v value '%129s' x`,
		`seq 1 100`,
	} {
		err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source})
		if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 128 reached") {
			t.Fatalf("source %q error = %v", source, err)
		}
	}
}

func TestSimulatorLimitsInputArrayMaterialization(t *testing.T) {
	simulator := NewBuilder().Limits(&Limits{MaxMemoryBytes: 64}).Build()
	for _, test := range []struct {
		source string
		stdin  string
	}{
		{source: `read -a values`, stdin: strings.Repeat("x ", 10)},
		{source: `mapfile -t values`, stdin: strings.Repeat("x\n", 10)},
	} {
		err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source, Stdin: []byte(test.stdin)})
		if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 64 reached") {
			t.Fatalf("source %q error = %v", test.source, err)
		}
	}
}

func TestSimulatorLimitsAggregateRuntimeStateBeforeHandler(t *testing.T) {
	callbacks := 0
	simulator := NewBuilder().
		Limits(&Limits{MaxMemoryBytes: 128}).
		Command("record", countInvocations(&callbacks)).
		Build()

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `printf -v first '%40s' x; printf -v second '%40s' x; record`,
	})
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 128 reached") {
		t.Fatalf("Simulate() error = %v", err)
	}
	if callbacks != 0 {
		t.Fatalf("callbacks = %d, want 0", callbacks)
	}
}

func TestSimulatorLimitsHandlerEnvironmentSnapshotBeforeDispatch(t *testing.T) {
	callbacks := 0
	simulator := NewBuilder().
		Limits(&Limits{MaxMemoryBytes: 128}).
		Command("record", countInvocations(&callbacks)).
		Build()

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `record`,
		Env:    map[string]string{"BIG": strings.Repeat("x", 100)},
	})
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 128 reached") {
		t.Fatalf("Simulate() error = %v", err)
	}
	if callbacks != 0 {
		t.Fatalf("callbacks = %d, want 0", callbacks)
	}
}

func TestSimulatorReturnsInputAndExecutionErrors(t *testing.T) {
	simulator := mustBuildSimulator(t, "lark-cli", successfulHandler())
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name    string
		run     func() error
		wantErr string
	}{
		{"parse", func() error {
			return simulator.Simulate(context.Background(), &SimulationRequest{Source: `if true; then`})
		}, "parse bash"},
		{"anonymous function", func() error {
			return simulator.Simulate(context.Background(), &SimulationRequest{Source: `()0`})
		}, "anonymous function declaration is not supported"},
		{"unsupported", func() error {
			return simulator.Simulate(context.Background(), &SimulationRequest{Source: `coproc lark-cli`})
		}, "unsupported Bash command"},
		{"canceled", func() error { return simulator.Simulate(canceled, &SimulationRequest{Source: `lark-cli never`}) }, context.Canceled.Error()},
		{"execution steps", func() error {
			return simulator.Simulate(context.Background(), &SimulationRequest{Source: `while :; do :; done`})
		}, "maximum execution step count 10000 reached"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestSimulatorCancellationPrecedesRequestMaterialization(t *testing.T) {
	simulator := mustBuildSimulator(t, "record", successfulHandler())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := simulator.Simulate(ctx, &SimulationRequest{Stdin: make([]byte, (4<<20)+1)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Simulate() error = %v, want context.Canceled", err)
	}
}

func TestSimulatorUsesConfiguredExecutionLimits(t *testing.T) {
	t.Run("custom limit", func(t *testing.T) {
		callbacks := 0
		simulator := NewBuilder().
			Limits(&Limits{MaxExecutionSteps: 1}).
			Command("record", countInvocations(&callbacks)).
			Build()
		err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `record; record`})
		if err == nil || !strings.Contains(err.Error(), "maximum execution step count 1 reached") {
			t.Fatalf("Simulate() error = %v", err)
		}
		if callbacks != 1 {
			t.Fatalf("callbacks = %d, want 1", callbacks)
		}
	})

	t.Run("last valid value wins", func(t *testing.T) {
		callbacks := 0
		simulator := NewBuilder().
			Limits(&Limits{MaxExecutionSteps: 1}).
			Limits(&Limits{MaxExecutionSteps: 2}).
			Command("record", countInvocations(&callbacks)).
			Build()
		if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `record; record`}); err != nil {
			t.Fatal(err)
		}
		if callbacks != 2 {
			t.Fatalf("callbacks = %d, want 2", callbacks)
		}
	})

	for _, builder := range []*Builder{
		NewBuilder().Limits(&Limits{}),
		new(Builder),
	} {
		simulator := builder.Build()
		if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `first; second`}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExecutionLimitDoesNotChangeRequestSemantics(t *testing.T) {
	var got []string
	simulator := NewBuilder().
		Limits(&Limits{MaxExecutionSteps: 1}).
		Command("record", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			got = argumentStrings(t, invocation)
			return &CommandResult{}, nil
		}).
		Build()
	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `record {a,b}`,
		Args:   []string{"one", "two"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("args = %#v", got)
	}
}
