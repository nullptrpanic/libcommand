package builtin

import (
	"context"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerCommand("wait", executeWait)
}

func executeWait(_ context.Context, execution *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return execution.ResultUnknown(&runtime.CommandResult{ExitCode: 1}, false, true, false), nil
	}
	if len(args) != 0 {
		return &runtime.CommandResult{Stderr: []byte("wait: job operands are not supported\n"), ExitCode: 2}, nil
	}
	return &runtime.CommandResult{}, nil
}
