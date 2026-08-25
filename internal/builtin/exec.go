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
		case "-a":
			if len(args) < 2 || args[1].Kind != runtime.ArgumentString {
				return shell.StopUnresolved("exec option \"-a\" requires a resolved name"), nil
			}
			args = args[2:]
		case "-l":
			args = args[1:]
		default:
			return shell.StopUnresolved(fmt.Sprintf("exec option %q is not supported", args[0].Value)), nil
		}
	}
	if len(args) == 0 {
		return commandResult(shell, nil, nil, 0), nil
	}
	if args[0].Kind != runtime.ArgumentString {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	if args[0].Value == "exec" {
		return shell.StopUnresolved("nested exec command is not supported"), nil
	}
	return shell.Replace(args[0].Value, args[1:], clearEnvironment), nil
}
