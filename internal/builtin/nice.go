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
		value, ok := wrapperArgument(invocation, index)
		if !ok {
			return unresolvedWrapper(shell)
		}
		if value == "--" {
			index++
			break
		}
		if value == "-n" || value == "--adjustment" {
			if _, ok := wrapperArgument(invocation, index+1); !ok {
				return unresolvedWrapper(shell)
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
			return unresolvedWrapper(shell)
		}
		break
	}
	return invokeExternalWrapper(shell, invocation, index, nil)
}
