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
		if value == "--foreground" || value == "--preserve-status" || value == "--verbose" {
			index++
			continue
		}
		if value == "-s" || value == "--signal" || value == "-k" || value == "--kill-after" {
			if _, ok := wrapperArgument(invocation, index+1); !ok {
				return unresolvedWrapper()
			}
			index += 2
			continue
		}
		if strings.HasPrefix(value, "--signal=") || strings.HasPrefix(value, "--kill-after=") || strings.HasPrefix(value, "-s") && len(value) > 2 || strings.HasPrefix(value, "-k") && len(value) > 2 {
			index++
			continue
		}
		return unresolvedWrapper()
	}
	if _, ok := wrapperArgument(invocation, index); !ok {
		return unresolvedWrapper()
	}
	return invokeExternalWrapper(shell, invocation, index+1, nil)
}
