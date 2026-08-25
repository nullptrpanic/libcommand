package builtin

import (
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func invokeExternalWrapper(shell *runtime.CommandContext, invocation *runtime.Invocation, target int, assignments map[string]string) (*runtime.CommandResult, error) {
	name, ok := wrapperArgument(invocation, target)
	if !ok {
		return unresolvedWrapper(shell)
	}
	return shell.InvokeWithEnvironment(name, invocation.Args[target+1:], false, nil, assignments), nil
}

func wrapperArgument(invocation *runtime.Invocation, index int) (string, bool) {
	if index >= len(invocation.Args) || invocation.Args[index].Kind != runtime.ArgumentString {
		return "", false
	}
	return invocation.Args[index].Value, true
}

func unresolvedWrapper(shell *runtime.CommandContext) (*runtime.CommandResult, error) {
	return unresolvedCommandResult(shell), nil
}

func shortFlagsOnly(value, allowed string) bool {
	if len(value) < 2 || value[0] != '-' || value[1] == '-' {
		return false
	}
	for _, option := range value[1:] {
		if !strings.ContainsRune(allowed, option) {
			return false
		}
	}
	return true
}
