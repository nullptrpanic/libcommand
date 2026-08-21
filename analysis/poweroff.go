package analysis

import (
	"context"

	"github.com/nullptrpanic/libcommand"
)

// Poweroff detects system power-off requests.
func Poweroff(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return commandResult(ctx, shell, invocation, RiskTypeDestructiveOperation)
}
