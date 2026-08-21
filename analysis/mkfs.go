package analysis

import (
	"context"

	"github.com/nullptrpanic/libcommand"
)

// Mkfs detects filesystem creation on a block device.
func Mkfs(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	arguments, concrete := concreteArguments(invocation.Args)
	riskType := RiskType("")
	if concrete && !informationalRequest(arguments) && hasBlockDevice(arguments, invocation.Dir, directoryUnresolved(invocation)) {
		riskType = RiskTypeDestructiveOperation
	}
	return commandResult(ctx, shell, invocation, riskType)
}

func informationalRequest(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "--help" || argument == "--version" {
			return true
		}
	}
	return false
}
