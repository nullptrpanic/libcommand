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
		argument := invocation.Args[index]
		if argument.Kind != runtime.ArgumentString {
			return unresolvedWrapperArgument(shell, invocation.Name)
		}
		value := argument.Value
		if value == "--" {
			index++
			break
		}
		if value == "-i" || value == "-o" || value == "-e" || value == "--input" || value == "--output" || value == "--error" {
			if index+1 >= len(invocation.Args) || invocation.Args[index+1].Kind != runtime.ArgumentString {
				return unresolvedWrapperArgument(shell, invocation.Name)
			}
			index += 2
			continue
		}
		if strings.HasPrefix(value, "--input=") || strings.HasPrefix(value, "--output=") || strings.HasPrefix(value, "--error=") || len(value) > 2 && value[0] == '-' && strings.ContainsRune("ioe", rune(value[1])) {
			index++
			continue
		}
		if strings.HasPrefix(value, "-") && value != "-" {
			return unsupportedWrapperOption(shell, invocation.Name, value)
		}
		break
	}
	return invokeExternalWrapper(shell, invocation, index, nil)
}
