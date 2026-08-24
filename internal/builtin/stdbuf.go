package builtin

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerExternalCandidate("stdbuf", executeStdbuf)
}

func executeStdbuf(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
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
		if value == "-i" || value == "-o" || value == "-e" || value == "--input" || value == "--output" || value == "--error" {
			if _, ok := wrapperArgument(invocation, index+1); !ok {
				return unresolvedWrapper()
			}
			index += 2
			continue
		}
		if strings.HasPrefix(value, "--input=") || strings.HasPrefix(value, "--output=") || strings.HasPrefix(value, "--error=") || len(value) > 2 && value[0] == '-' && strings.ContainsRune("ioe", rune(value[1])) {
			index++
			continue
		}
		if strings.HasPrefix(value, "-") && value != "-" {
			return unresolvedWrapper()
		}
		break
	}
	return invokeExternalWrapper(shell, invocation, index, nil)
}
