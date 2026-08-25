package builtin

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerExternalCandidate("taskset", executeTaskset)
}

func executeTaskset(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	index := 0
	pidMode := false
	for index < len(invocation.Args) {
		value, ok := wrapperArgument(invocation, index)
		if !ok {
			return unresolvedWrapper(shell)
		}
		if value == "--" {
			index++
			break
		}
		if !strings.HasPrefix(value, "-") || value == "-" {
			break
		}
		if value == "--pid" || value[1] != '-' && strings.Contains(value[1:], "p") {
			pidMode = true
		}
		if value == "--all-tasks" || value == "--pid" || value == "--cpu-list" || shortFlagsOnly(value, "apc") {
			index++
			continue
		}
		return unresolvedWrapper(shell)
	}
	if pidMode {
		return shell.StopUnresolved("taskset PID mode does not execute a nested command"), nil
	}
	if _, ok := wrapperArgument(invocation, index); !ok {
		return unresolvedWrapper(shell)
	}
	return invokeExternalWrapper(shell, invocation, index+1, nil)
}
