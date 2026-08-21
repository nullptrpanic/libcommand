package builtin

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerExternalCandidate("timeout", executeTimeout)
}

func executeTimeout(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	index := 0
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
		if value == "--foreground" || value == "--preserve-status" || value == "--verbose" {
			index++
			continue
		}
		if value == "-s" || value == "--signal" || value == "-k" || value == "--kill-after" {
			if index+1 >= len(invocation.Args) || invocation.Args[index+1].Kind != runtime.ArgumentString {
				return unresolvedWrapperArgument(shell, invocation.Name)
			}
			index += 2
			continue
		}
		if strings.HasPrefix(value, "--signal=") || strings.HasPrefix(value, "--kill-after=") || strings.HasPrefix(value, "-s") && len(value) > 2 || strings.HasPrefix(value, "-k") && len(value) > 2 {
			index++
			continue
		}
		return unsupportedWrapperOption(shell, invocation.Name, value)
	}
	if index >= len(invocation.Args) || invocation.Args[index].Kind != runtime.ArgumentString {
		return unresolvedWrapperArgument(shell, invocation.Name)
	}
	return invokeExternalWrapper(shell, invocation, index+1, nil)
}
