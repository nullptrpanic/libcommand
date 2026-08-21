package analysis

import (
	"context"

	"github.com/nullptrpanic/libcommand"
)

// Halt detects system halt requests.
func Halt(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return commandResult(ctx, shell, invocation, RiskTypeDestructiveOperation)
}
