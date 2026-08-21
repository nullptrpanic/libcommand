package builtin

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerExternalCandidate("chrt", executeChrt)
}

func executeChrt(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
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
		if value == "-p" || value == "--pid" {
			pidMode = true
			index++
			continue
		}
		if value == "-T" || value == "--sched-runtime" || value == "-P" || value == "--sched-period" || value == "-D" || value == "--sched-deadline" {
			if index+1 >= len(invocation.Args) || invocation.Args[index+1].Kind != runtime.ArgumentString {
				return unresolvedWrapperArgument(shell, invocation.Name)
			}
			index += 2
			continue
		}
		if value == "--batch" || value == "--deadline" || value == "--fifo" || value == "--idle" || value == "--other" || value == "--rr" || value == "--reset-on-fork" || value == "--all-tasks" || value == "--max" || value == "--verbose" || shortFlagsOnly(value, "bdfiorRavm") {
			index++
			continue
		}
		return unsupportedWrapperOption(shell, invocation.Name, value)
	}
	if pidMode {
		return shell.StopUnresolved("chrt PID mode does not execute a nested command"), nil
	}
	if index >= len(invocation.Args) || invocation.Args[index].Kind != runtime.ArgumentString {
		return unresolvedWrapperArgument(shell, invocation.Name)
	}
	return invokeExternalWrapper(shell, invocation, index+1, nil)
}
