package analysis

import (
	"context"

	"github.com/nullptrpanic/libcommand"
)

// Reboot detects system reboot requests.
func Reboot(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return argumentRiskResult(ctx, shell, invocation, powerRequest, RiskTypeDestructiveOperation)
}

func powerRequest(arguments []string) bool { return !informationalRequest(arguments) }
