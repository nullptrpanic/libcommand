package builtin

import (
	"context"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerCommand("pwd", executeStatePWD)
}

func executeStatePWD(_ context.Context, execution *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return unresolvedStderrCommandResult(execution, 1), nil
	}
	for _, argument := range args {
		if argument != "-L" && argument != "-P" && argument != "--" {
			return commandResult(execution, nil, []byte("pwd: invalid option\n"), 2), nil
		}
	}
	directory, unknown := execution.Directory()
	if unknown {
		return uncertainCommandResult(execution, nil, nil, 0, true, false, false), nil
	}
	length, ok := materialize.Add(len(directory), 1, execution.MaxMemoryBytes())
	if !ok {
		return nil, materialize.LimitError(execution.MaxMemoryBytes())
	}
	output := make([]byte, 0, length)
	output = append(output, directory...)
	output = append(output, '\n')
	return commandResult(execution, output, nil, 0), nil
}
