package analysis

import (
	"context"

	"github.com/nullptrpanic/libcommand"
)

// Mkfifo records concrete FIFO paths for stateful stream analysis.
func Mkfifo(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	observeMkfifo(ctx, shell, invocation)
	return commandResult(ctx, shell, invocation, "")
}
