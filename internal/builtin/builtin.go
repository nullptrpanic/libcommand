package builtin

import (
	"context"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerCandidate("builtin", executeBuiltin)
}

func executeBuiltin(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args := invocation.Args
	if len(args) == 0 {
		return &runtime.CommandResult{}, nil
	}
	if args[0].Kind != runtime.ArgumentString {
		return shell.ResultUnknown(&runtime.CommandResult{ExitCode: 1}, false, true, false), nil
	}
	return shell.Invoke(args[0].Value, args[1:], true), nil
}
