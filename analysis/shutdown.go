package analysis

import (
	"context"

	"github.com/nullptrpanic/libcommand"
)

// Shutdown detects shutdown requests while allowing cancellation commands.
func Shutdown(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return argumentRiskResult(ctx, shell, invocation, shutdownRequest, "system power control")
}

func shutdownRequest(arguments []string) bool {
	return !shutdownCancellation(arguments)
}

func shutdownCancellation(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "-c" || argument == "--cancel" {
			return true
		}
	}
	return false
}
