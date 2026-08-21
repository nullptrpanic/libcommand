package analysis

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

// Wipefs detects removal of filesystem signatures from a block device.
func Wipefs(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	arguments, concrete := concreteArguments(invocation.Args)
	riskType := RiskType("")
	if concrete && wipefsMutates(arguments) && hasBlockDevice(arguments, invocation.Dir, directoryUnresolved(invocation)) {
		riskType = RiskTypeDestructiveOperation
	}
	return commandResult(ctx, shell, invocation, riskType)
}

func wipefsMutates(arguments []string) bool {
	mutates := false
	for _, argument := range arguments {
		if argument == "--no-act" || strings.HasPrefix(argument, "-") && !strings.HasPrefix(argument, "--") && strings.ContainsRune(argument[1:], 'n') {
			return false
		}
		if argument == "-a" || argument == "--all" || argument == "-o" || argument == "--offset" || strings.HasPrefix(argument, "--offset=") {
			mutates = true
			continue
		}
		if strings.HasPrefix(argument, "-") && !strings.HasPrefix(argument, "--") && strings.ContainsAny(argument[1:], "ao") {
			mutates = true
		}
	}
	return mutates
}
