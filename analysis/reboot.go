package analysis

import (
	"context"

	"github.com/nullptrpanic/libcommand"
)

// Reboot detects system reboot requests.
func Reboot(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return commandResult(ctx, shell, invocation, "system power control")
}
