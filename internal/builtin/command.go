package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerCandidate("command", executeCommand)
}

func executeCommand(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args := invocation.Args
	optionsTerminated := false
	for len(args) > 0 && args[0].Kind == runtime.ArgumentString {
		if args[0].Value == "--" {
			args = args[1:]
			optionsTerminated = true
			break
		}
		if args[0].Value != "-p" {
			break
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return &runtime.CommandResult{}, nil
	}
	if args[0].Kind != runtime.ArgumentString {
		return shell.ResultUnknown(&runtime.CommandResult{ExitCode: 1}, false, true, false), nil
	}
	if !optionsTerminated && (args[0].Value == "-v" || args[0].Value == "-V") {
		names := make([]string, 0, len(args)-1)
		for _, argument := range args[1:] {
			if argument.Kind != runtime.ArgumentString {
				return shell.ResultUnknown(&runtime.CommandResult{ExitCode: 1}, true, false, false), nil
			}
			names = append(names, argument.Value)
		}
		mode := "command-name"
		if args[0].Value == "-V" {
			mode = "command-verbose"
		}
		return lookupCommands(shell, names, mode), nil
	}
	if !optionsTerminated && strings.HasPrefix(args[0].Value, "-") {
		return shell.StopUnresolved(fmt.Sprintf("command option %q is not supported", args[0].Value)), nil
	}
	return shell.Invoke(args[0].Value, args[1:], false), nil
}
