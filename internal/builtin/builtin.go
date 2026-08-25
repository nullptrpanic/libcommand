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
		return commandResult(shell, nil, nil, 0), nil
	}
	if args[0].Kind != runtime.ArgumentString {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	return shell.Invoke(args[0].Value, args[1:], true), nil
}
