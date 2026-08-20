package analysis

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

func netcatCommand(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return argumentRiskResult(ctx, shell, invocation, netcatExecRisk, "network utility executes a local program")
}

func netcatExecRisk(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "-e" || argument == "-c" || argument == "--exec" || argument == "--sh-exec" || strings.HasPrefix(argument, "--exec=") || strings.HasPrefix(argument, "--sh-exec=") {
			return true
		}
	}
	return false
}
