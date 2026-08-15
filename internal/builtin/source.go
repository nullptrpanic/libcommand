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
		return shell.ResultUnknown(&runtime.CommandResult{ExitCode: 1}, false, true, false), nil
	}
	if len(args) == 0 {
		return &runtime.CommandResult{Stderr: []byte("source: filename argument required\n"), ExitCode: 2}, nil
	}
	filename := shell.ResolvePath(args[0])
	contents, unknown, exists := shell.ReadFile(filename)
	if !exists {
		return &runtime.CommandResult{
			Stderr:   []byte(fmt.Sprintf("source: %s: No such file or directory\n", args[0])),
			ExitCode: 1,
		}, nil
	}
	if unknown {
		return shell.StopUnresolved("source contents depend on unresolved command output"), nil
	}
	return shell.Source(string(contents), args[0], args[1:]), nil
}
