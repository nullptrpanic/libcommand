package analysis

import (
	"context"

	"github.com/nullptrpanic/libcommand"
)

// Netcat detects netcat options that execute a local program.
func Netcat(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return netcatCommand(ctx, shell, invocation)
}
