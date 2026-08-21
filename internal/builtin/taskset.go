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
		argument := invocation.Args[index]
		if argument.Kind != runtime.ArgumentString {
			return unresolvedWrapperArgument(shell, invocation.Name)
		}
		value := argument.Value
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
		return unsupportedWrapperOption(shell, invocation.Name, value)
	}
	if pidMode {
		return shell.StopUnresolved("taskset PID mode does not execute a nested command"), nil
	}
	if index >= len(invocation.Args) || invocation.Args[index].Kind != runtime.ArgumentString {
		return unresolvedWrapperArgument(shell, invocation.Name)
	}
	return invokeExternalWrapper(shell, invocation, index+1, nil)
}
