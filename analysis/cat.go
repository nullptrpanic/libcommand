package analysis

import (
	"context"

	"github.com/nullptrpanic/libcommand"
)

// Cat records reads from a FIFO previously observed by the same Session.
func Cat(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	observeCat(ctx, shell, invocation)
	return commandResult(ctx, shell, invocation, "")
}
