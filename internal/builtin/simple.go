package builtin

import (
	"context"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func executeTrue(ctx context.Context, _ *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &runtime.CommandResult{}, nil
}

func executeFalse(ctx context.Context, _ *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &runtime.CommandResult{ExitCode: 1}, nil
}
