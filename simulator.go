package libcommand

import (
	"context"
	"fmt"

	shellruntime "github.com/nullptrpanic/libcommand/internal/runtime"
)

// Simulate parses and evaluates one Bash program in an isolated virtual state.
// Registered external commands are observable through their implementations.
func (s *Simulator) Simulate(ctx context.Context, request *SimulationRequest) error {
	return s.simulate(ctx, request, nil, nil)
}

// SimulateTrace runs one simulation and synchronously streams optional trace
// events. Returning false from observer disables further events without
// changing the simulation result.
func (s *Simulator) SimulateTrace(ctx context.Context, request *SimulationRequest, observer TraceObserver) error {
	return s.simulate(ctx, request, nil, observer)
}

// SimulateTraceWithOptions runs one simulation with optional trace-only state
// snapshots. Snapshot retention is bounded by options and does not change Shell
// evaluation.
func (s *Simulator) SimulateTraceWithOptions(ctx context.Context, request *SimulationRequest, options *TraceOptions, observer TraceObserver) error {
	return s.simulate(ctx, request, options, observer)
}

func (s *Simulator) simulate(ctx context.Context, request *SimulationRequest, traceOptions *TraceOptions, observer TraceObserver) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if request == nil {
		return fmt.Errorf("simulation request is nil")
	}
	limits := limitsWithDefaults(s.limits)
	if err := checkUntrustedRequestMaterialization(request, limits.MaxMemoryBytes); err != nil {
		return err
	}

	file, err := shellruntime.Parse(ctx, request.Source, "command.sh")
	if err != nil {
		return fmt.Errorf("parse bash: %w", err)
	}
	execution := shellruntime.NewExecutionContext(ctx, &shellruntime.Config{
		LookupCommand:     s.lookupCommand,
		MaxExecutionSteps: limits.MaxExecutionSteps,
		MaxMemoryBytes:    limits.MaxMemoryBytes,
		Trace:             observer,
		TraceOptions:      traceOptions,
	})
	return execution.Execute(file, &shellruntime.Request{
		Source: request.Source,
		Env:    request.Env,
		Args:   request.Args,
		Stdin:  request.Stdin,
	})
}

func (s *Simulator) lookupCommand(name string) *shellruntime.CommandDefinition {
	if command := s.commands[name]; command != nil {
		return command
	}
	return s.commands["*"]
}
