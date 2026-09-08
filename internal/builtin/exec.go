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
	var argv0 *string
	login := false
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
			argv0 = &args[1].Value
			args = args[2:]
		case "-l":
			login = true
			args = args[1:]
		default:
			return shell.StopUnresolved(fmt.Sprintf("exec option %q is not supported", args[0].Value)), nil
		}
	}
	if len(args) == 0 {
		shell.PersistRedirections()
		return commandResult(shell, nil, nil, 0), nil
	}
	if args[0].Kind != runtime.ArgumentString {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	if args[0].Value == "exec" {
		return shell.StopUnresolved("nested exec command is not supported"), nil
	}
	result := shell.Replace(args[0].Value, args[1:], clearEnvironment)
	if argv0 != nil || login {
		name := args[0].Value
		if argv0 != nil {
			name = *argv0
		}
		if login {
			name = "-" + name
		}
		result.WithArgv0(name)
	}
	return result, nil
}
