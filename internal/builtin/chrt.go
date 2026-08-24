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
		value, ok := wrapperArgument(invocation, index)
		if !ok {
			return unresolvedWrapper()
		}
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
			if _, ok := wrapperArgument(invocation, index+1); !ok {
				return unresolvedWrapper()
			}
			index += 2
			continue
		}
		if value == "--batch" || value == "--deadline" || value == "--fifo" || value == "--idle" || value == "--other" || value == "--rr" || value == "--reset-on-fork" || value == "--all-tasks" || value == "--max" || value == "--verbose" || shortFlagsOnly(value, "bdfiorRavm") {
			index++
			continue
		}
		return unresolvedWrapper()
	}
	if pidMode {
		return shell.StopUnresolved("chrt PID mode does not execute a nested command"), nil
	}
	if _, ok := wrapperArgument(invocation, index); !ok {
		return unresolvedWrapper()
	}
	return invokeExternalWrapper(shell, invocation, index+1, nil)
}
