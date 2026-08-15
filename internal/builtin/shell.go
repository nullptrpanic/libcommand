package builtin

import (
	"context"
	"fmt"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func executeShell(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	name := invocation.Name
	if !concrete {
		return shell.StopUnresolved(fmt.Sprintf("%s script contents depend on unresolved command output", name)), nil
	}
	program := &runtime.ShellProgram{Options: make(map[string]bool)}
	index := 0
	commandString := false
	stdinSource := false
	for index < len(args) && len(args[index]) > 1 && (args[index][0] == '-' || args[index][0] == '+') {
		argument := args[index]
		if argument == "--" {
			index++
			break
		}
		if argument == "--noprofile" || argument == "--norc" {
			index++
			continue
		}
		enabled := argument[0] == '-'
		for position := 1; position < len(argument); position++ {
			switch option := argument[position]; option {
			case 'c':
				if !enabled {
					return unsupportedShellOption(shell, name, argument)
				}
				commandString = true
				program.Options["command-string"] = true
			case 'l':
				// Login startup files are outside the virtual Shell state.
			case 's':
				if !enabled {
					return unsupportedShellOption(shell, name, argument)
				}
				stdinSource = true
			case 'o':
				if position+1 != len(argument) || index+1 >= len(args) || !isNamedShellOption(args[index+1]) {
					return unsupportedShellOption(shell, name, argument)
				}
				program.Options[args[index+1]] = enabled
				index++
			case 'O':
				if position+1 != len(argument) || index+1 >= len(args) || !isShoptOption(args[index+1]) {
					return unsupportedShellOption(shell, name, argument)
				}
				program.Options[args[index+1]] = enabled
				index++
			default:
				optionName, exists := shortShellOptionName(option)
				if !exists {
					return unsupportedShellOption(shell, name, argument)
				}
				program.Options[optionName] = enabled
			}
		}
		index++
		if commandString {
			break
		}
	}
	if commandString {
		if index >= len(args) {
			return shell.StopUnresolved(fmt.Sprintf("%s requires -c source or a virtual script path", name)), nil
		}
		program.Source = args[index]
		index++
		program.Name = name
		if index < len(args) {
			program.Name = args[index]
			index++
		}
		program.Arguments = append([]string(nil), args[index:]...)
		return shell.RunShell(program), nil
	}
	if stdinSource || index >= len(args) {
		input, unresolved := shell.Input()
		if unresolved {
			return nil, nil
		}
		program.Source = string(input)
		program.Name = name
		program.Arguments = append([]string(nil), args[index:]...)
		shell.SetInput(nil, false)
		return shell.RunShell(program), nil
	}
	program.Name = args[index]
	filename := shell.ResolvePath(program.Name)
	contents, unknown, exists := shell.ReadFile(filename)
	if !exists {
		return &runtime.CommandResult{
			Stderr:   []byte(fmt.Sprintf("%s: %s: No such file or directory\n", name, program.Name)),
			ExitCode: 127,
		}, nil
	}
	if unknown {
		return shell.StopUnresolved(fmt.Sprintf("%s script contents depend on unresolved command output", name)), nil
	}
	program.Source = string(contents)
	program.Arguments = append([]string(nil), args[index+1:]...)
	return shell.RunShell(program), nil
}

func unsupportedShellOption(execution *runtime.CommandContext, shell, option string) (*runtime.CommandResult, error) {
	return execution.StopUnresolved(fmt.Sprintf("%s: unsupported option %q", shell, option)), nil
}

func shortShellOptionName(option byte) (string, bool) {
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
	return name, exists
}

func isNamedShellOption(name string) bool {
	for _, option := range namedShellOptions {
		if name == option {
			return true
		}
	}
	return false
}
