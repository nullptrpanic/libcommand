package analysis

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

func netcatCommand(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	if observeNetcat(ctx, shell, invocation) {
		return commandResult(ctx, shell, invocation, RiskTypeReverseShell)
	}
	return argumentRiskResult(ctx, shell, invocation, netcatExecRisk, RiskTypeReverseShell)
}

func netcatExecRisk(arguments []string) bool {
	for index, argument := range arguments {
		switch {
		case argument == "--sh-exec" || strings.HasPrefix(argument, "--sh-exec="):
			return true
		case argument == "-e" || argument == "-c" || argument == "--exec":
			if index+1 < len(arguments) && containsShellExecutable(arguments[index+1]) {
				return true
			}
		case strings.HasPrefix(argument, "--exec="):
			if containsShellExecutable(strings.TrimPrefix(argument, "--exec=")) {
				return true
			}
		}
	}
	return false
}
