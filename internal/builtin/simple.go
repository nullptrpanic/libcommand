package builtin

import (
	"context"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func executeTrue(ctx context.Context, shell *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return commandResult(shell, nil, nil, 0), nil
}

func executeFalse(ctx context.Context, shell *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return commandResult(shell, nil, nil, 1), nil
}
