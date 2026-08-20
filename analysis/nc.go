package analysis

import (
	"context"

	"github.com/nullptrpanic/libcommand"
)

// NC detects nc options that execute a local program.
func NC(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return netcatCommand(ctx, shell, invocation)
}
