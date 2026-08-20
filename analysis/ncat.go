package analysis

import (
	"context"

	"github.com/nullptrpanic/libcommand"
)

// Ncat detects ncat options that execute a local program.
func Ncat(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return netcatCommand(ctx, shell, invocation)
}
