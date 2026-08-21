package analysis

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

// DD detects writes to block devices.
func DD(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	arguments, concrete := concreteArguments(invocation.Args)
	riskType := RiskType("")
	if concrete && ddWritesBlockDevice(arguments, shell.Redirects(), invocation.Dir, directoryUnresolved(invocation)) {
		riskType = RiskTypeDestructiveOperation
	}
	return commandResult(ctx, shell, invocation, riskType)
}

func ddWritesBlockDevice(arguments []string, redirects []*libcommand.Redirect, directory string, directoryUnknown bool) bool {
	for _, argument := range arguments {
		if output, found := strings.CutPrefix(argument, "of="); found && blockDevicePath(output, directory, directoryUnknown) {
			return true
		}
	}
	for _, redirect := range redirects {
		if redirect.FD == 1 && !redirect.Unresolved && blockDevicePath(redirect.Target, directory, directoryUnknown) {
			return true
		}
	}
	return false
}
