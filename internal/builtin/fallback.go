package builtin

import (
	"context"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	register("*", executeFallback)
}

func executeFallback(ctx context.Context, shell *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return unresolvedCommandResult(shell), nil
}
