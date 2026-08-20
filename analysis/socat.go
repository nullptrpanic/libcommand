package analysis

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

// Socat detects socat addresses that execute a local program.
func Socat(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return argumentRiskResult(ctx, shell, invocation, socatExecRisk, "network utility executes a local program")
}

func socatExecRisk(arguments []string) bool {
	for _, argument := range arguments {
		upper := strings.ToUpper(argument)
		if strings.HasPrefix(upper, "EXEC:") || strings.HasPrefix(upper, "SYSTEM:") {
			return true
		}
	}
	return false
}
