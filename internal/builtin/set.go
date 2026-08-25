package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

var namedShellOptions = [...]string{
	"allexport",
	"errexit",
	"errtrace",
	"noglob",
	"nounset",
	"pipefail",
	"verbose",
	"xtrace",
}

func init() {
	registerCommand("set", executeSet)
}

func executeSet(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	if len(args) == 0 {
		stdout, stderr, exitCode, stdoutUnknown := queryShellVariables(shell)
		return uncertainCommandResult(shell, stdout, stderr, exitCode, stdoutUnknown, false, false), nil
	}
	setArguments := false
	remaining := args
	for len(remaining) > 0 {
		argument := remaining[0]
		if argument == "--" {
			setArguments = true
			remaining = remaining[1:]
			break
		}
		if len(argument) < 2 || argument[0] != '-' && argument[0] != '+' {
			setArguments = true
			break
		}
		enabled := argument[0] == '-'
		if argument == "-" || argument == "+" {
			remaining = remaining[1:]
			continue
		}
		for index := 1; index < len(argument); index++ {
			option := argument[index]
			if option == 'o' {
				if index+1 < len(argument) {
					return commandResult(shell, nil, []byte("set: invalid option name\n"), 2), nil
				}
				if len(remaining) == 1 {
					return queryNamedShellOptions(shell, enabled), nil
				}
				if !shell.SetOption(remaining[1], enabled) {
					return commandResult(shell, nil, []byte(fmt.Sprintf("set: %s: invalid option name\n", remaining[1])), 2), nil
				}
				remaining = remaining[1:]
				break
			}
			if !setShortShellOption(shell, option, enabled) {
				return commandResult(shell, nil, []byte(fmt.Sprintf("set: -%c: invalid option\n", option)), 2), nil
			}
		}
		remaining = remaining[1:]
	}
	if setArguments {
		shell.ReplacePositionalArguments(remaining)
	}
	return commandResult(shell, nil, nil, 0), nil
}

func setShortShellOption(shell *runtime.CommandContext, option byte, enabled bool) bool {
	names := map[byte]string{
		'a': "allexport",
		'e': "errexit",
		'E': "errtrace",
		'u': "nounset",
		'f': "noglob",
		'x': "xtrace",
		'v': "verbose",
	}
	name, exists := names[option]
	return exists && shell.SetOption(name, enabled)
}

func queryShellVariables(shell *runtime.CommandContext) (stdout, stderr []byte, exitCode int, unknown bool) {
	maximum := shell.MaxMemoryBytes()
	var output strings.Builder
	shell.EachVariable(func(name string, value *expand.Variable) bool {
		if !syntax.ValidName(name) {
			return true
		}
		line := name + "=" + quoteShellWord(value.String()) + "\n"
		if _, ok := materialize.Add(output.Len(), len(line), maximum); !ok {
			stderr = []byte("set: output exceeds materialization limit\n")
			exitCode = 1
			return false
		}
		unknown = unknown || shell.VariableUnknown(name)
		output.WriteString(line)
		return true
	})
	if exitCode == 0 {
		stdout = []byte(output.String())
	}
	return stdout, stderr, exitCode, unknown
}

func queryNamedShellOptions(shell *runtime.CommandContext, table bool) *runtime.CommandResult {
	var output strings.Builder
	for _, name := range namedShellOptions {
		enabled, _ := shell.Option(name)
		if table {
			fmt.Fprintf(&output, "%-15s %s\n", name, optionState(enabled))
			continue
		}
		operator := "+"
		if enabled {
			operator = "-"
		}
		fmt.Fprintf(&output, "set %so %s\n", operator, name)
	}
	return commandResult(shell, []byte(output.String()), nil, 0)
}

func quoteShellWord(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
