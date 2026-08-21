package builtin

import (
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func invokeExternalWrapper(shell *runtime.CommandContext, invocation *runtime.Invocation, target int, assignments map[string]string) (*runtime.CommandResult, error) {
	if target >= len(invocation.Args) {
		return unresolvedWrapper(), nil
	}
	argument := invocation.Args[target]
	if argument.Kind != runtime.ArgumentString {
		return unresolvedWrapper(), nil
	}
	return shell.InvokeWithEnvironment(argument.Value, invocation.Args[target+1:], false, nil, assignments), nil
}

func unresolvedWrapperArgument(_ *runtime.CommandContext, _ string) (*runtime.CommandResult, error) {
	return unresolvedWrapper(), nil
}

func unsupportedWrapperOption(_ *runtime.CommandContext, _, _ string) (*runtime.CommandResult, error) {
	return unresolvedWrapper(), nil
}

func unresolvedWrapper() *runtime.CommandResult {
	return &runtime.CommandResult{Unresolved: true}
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
