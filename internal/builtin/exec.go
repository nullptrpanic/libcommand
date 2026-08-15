package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerCandidate("exec", executeExec)
}

func executeExec(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args := invocation.Args
	clearEnvironment := false
options:
	for len(args) > 0 && args[0].Kind == runtime.ArgumentString && strings.HasPrefix(args[0].Value, "-") {
		switch args[0].Value {
		case "--":
			args = args[1:]
			break options
		case "-c":
			clearEnvironment = true
			args = args[1:]
		case "-a", "-l":
			return shell.StopUnresolved(fmt.Sprintf("exec option %q is not supported", args[0].Value)), nil
		default:
			return shell.StopUnresolved(fmt.Sprintf("exec option %q is not supported", args[0].Value)), nil
		}
	}
	if len(args) == 0 {
		return shell.StopUnresolved("exec without a command is not supported"), nil
	}
	if args[0].Kind != runtime.ArgumentString {
		return shell.ResultUnknown(&runtime.CommandResult{ExitCode: 1}, false, true, false), nil
	}
	if args[0].Value == "exec" {
		return shell.StopUnresolved("nested exec command is not supported"), nil
	}
	return shell.Replace(args[0].Value, args[1:], clearEnvironment), nil
}
