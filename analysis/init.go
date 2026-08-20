package analysis

import (
	"context"

	"github.com/nullptrpanic/libcommand"
)

// Init detects init runlevels that halt or reboot the system.
func Init(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return argumentRiskResult(ctx, shell, invocation, powerRunlevel, "system power control")
}
