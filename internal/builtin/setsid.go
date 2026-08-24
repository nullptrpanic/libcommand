package builtin

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerExternalCandidate("setsid", executeSetsid)
}

func executeSetsid(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
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
		if value == "--fork" || value == "--ctty" || value == "--wait" || shortFlagsOnly(value, "fcw") {
			index++
			continue
		}
		return unresolvedWrapper()
	}
	return invokeExternalWrapper(shell, invocation, index, nil)
}
