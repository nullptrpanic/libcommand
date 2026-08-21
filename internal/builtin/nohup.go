package builtin

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerExternalCandidate("nohup", executeNohup)
}

func executeNohup(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	index := 0
	if len(invocation.Args) != 0 {
		argument := invocation.Args[0]
		if argument.Kind != runtime.ArgumentString {
			return unresolvedWrapperArgument(shell, invocation.Name)
		}
		if argument.Value == "--" {
			index++
		} else if strings.HasPrefix(argument.Value, "-") && argument.Value != "-" {
			return unsupportedWrapperOption(shell, invocation.Name, argument.Value)
		}
	}
	return invokeExternalWrapper(shell, invocation, index, nil)
}
