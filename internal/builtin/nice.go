package builtin

import (
	"context"
	"strconv"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerExternalCandidate("nice", executeNice)
}

func executeNice(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
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
		if value == "-n" || value == "--adjustment" {
			if index+1 >= len(invocation.Args) || invocation.Args[index+1].Kind != runtime.ArgumentString {
				return unresolvedWrapperArgument(shell, invocation.Name)
			}
			index += 2
			continue
		}
		if strings.HasPrefix(value, "--adjustment=") {
			index++
			continue
		}
		if strings.HasPrefix(value, "-") && len(value) > 1 {
			if _, err := strconv.Atoi(value[1:]); err == nil {
				index++
				continue
			}
			return unsupportedWrapperOption(shell, invocation.Name, value)
		}
		break
	}
	return invokeExternalWrapper(shell, invocation, index, nil)
}
