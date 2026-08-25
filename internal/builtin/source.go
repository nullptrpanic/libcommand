package builtin

import (
	"context"
	"fmt"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerRestoreAlways("source", executeSource)
}

func executeSource(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	if len(args) == 0 {
		return commandResult(shell, nil, []byte("source: filename argument required\n"), 2), nil
	}
	filename := shell.ResolvePath(args[0])
	contents, unknown, exists := shell.ReadFile(filename)
	if !exists {
		return commandResult(shell, nil, []byte(fmt.Sprintf("source: %s: No such file or directory\n", args[0])), 1), nil
	}
	if unknown {
		return shell.StopUnresolved("source contents depend on unresolved command output"), nil
	}
	return shell.Source(string(contents), args[0], args[1:]), nil
}
