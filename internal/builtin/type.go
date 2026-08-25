package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerCommand("type", executeType)
}

func executeType(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	mode := "verbose"
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "-t":
			mode = "type"
		case "--":
			return lookupCommands(shell, args[1:], mode), nil
		default:
			return commandResult(shell, nil, []byte("type: unsupported option\n"), 2), nil
		}
		args = args[1:]
	}
	return lookupCommands(shell, args, mode), nil
}

func lookupCommands(shell *runtime.CommandContext, names []string, mode string) *runtime.CommandResult {
	if len(names) == 0 {
		return commandResult(shell, nil, nil, 1)
	}
	var output strings.Builder
	found := false
	for _, name := range names {
		kind := shell.LookupCommand(name)
		if kind == runtime.CommandMissing {
			continue
		}
		found = true
		kindName := commandKindName(kind)
		switch mode {
		case "type":
			output.WriteString(kindName)
		case "verbose", "command-verbose":
			switch kind {
			case runtime.CommandFunction:
				fmt.Fprintf(&output, "%s is a function", name)
			case runtime.CommandBuiltin:
				fmt.Fprintf(&output, "%s is a shell builtin", name)
			default:
				fmt.Fprintf(&output, "%s is %s", name, name)
			}
		default:
			output.WriteString(name)
		}
		output.WriteByte('\n')
	}
	exitCode := 0
	if !found {
		exitCode = 1
	}
	return commandResult(shell, []byte(output.String()), nil, exitCode)
}

func commandKindName(kind runtime.CommandKind) string {
	switch kind {
	case runtime.CommandFunction:
		return "function"
	case runtime.CommandBuiltin:
		return "builtin"
	case runtime.CommandFile:
		return "file"
	default:
		return ""
	}
}
