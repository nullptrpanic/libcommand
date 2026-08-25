package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

var shoptOptions = [...]string{
	"dotglob",
	"extglob",
	"globstar",
	"inherit_errexit",
	"nocaseglob",
	"nullglob",
}

func init() {
	registerCommand("shopt", executeShopt)
}

func executeShopt(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	mode := 0
	query := false
	index := 0
	for index < len(args) && strings.HasPrefix(args[index], "-") {
		switch args[index] {
		case "-s":
			mode = 1
		case "-u":
			mode = -1
		case "-q":
			query = true
		case "--":
			index++
			goto options
		default:
			return commandResult(shell, nil, []byte("shopt: invalid option\n"), 2), nil
		}
		index++
	}

options:
	names := args[index:]
	listMode := len(names) == 0 && mode != 0
	if len(names) == 0 {
		names = shoptOptions[:]
	}
	var output strings.Builder
	exitCode := 0
	for _, name := range names {
		enabled, exists := shell.Option(name)
		if !exists || !isShoptOption(name) {
			return commandResult(shell, nil, []byte(fmt.Sprintf("shopt: %s: invalid shell option name\n", name)), 1), nil
		}
		if listMode {
			if enabled == (mode > 0) && !query {
				flag := "-u"
				if mode > 0 {
					flag = "-s"
				}
				fmt.Fprintf(&output, "shopt %s %s\n", flag, name)
			}
			continue
		}
		if mode == 0 {
			if !enabled {
				exitCode = 1
			}
			if !query {
				fmt.Fprintf(&output, "%-15s\t%s\n", name, optionState(enabled))
			}
			continue
		}
		shell.SetOption(name, mode > 0)
	}
	return commandResult(shell, []byte(output.String()), nil, exitCode), nil
}

func isShoptOption(name string) bool {
	for _, option := range shoptOptions {
		if name == option {
			return true
		}
	}
	return false
}

func optionState(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}
