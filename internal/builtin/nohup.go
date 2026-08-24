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
		value, ok := wrapperArgument(invocation, 0)
		if !ok {
			return unresolvedWrapper()
		}
		if value == "--" {
			index++
		} else if strings.HasPrefix(value, "-") && value != "-" {
			return unresolvedWrapper()
		}
	}
	return invokeExternalWrapper(shell, invocation, index, nil)
}
