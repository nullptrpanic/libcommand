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
		return unresolvedStderrCommandResult(execution, 1), nil
	}
	if len(args) != 0 {
		return commandResult(execution, nil, []byte("wait: job operands are not supported\n"), 2), nil
	}
	return commandResult(execution, nil, nil, 0), nil
}
