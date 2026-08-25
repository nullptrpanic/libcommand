package builtin

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	register("rm", executeRM)
}

func executeRM(ctx context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	arguments, concrete := concreteArguments(invocation)
	if !concrete {
		return unresolvedCommandResult(shell), nil
	}

	recursive := false
	force := false
	operands := make([]string, 0, len(arguments))
	options := true
	for _, argument := range arguments {
		if options && argument == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(argument, "--") {
			switch argument {
			case "--force":
				force = true
			case "--recursive":
				recursive = true
			default:
				return commandResult(shell, nil, []byte(fmt.Sprintf("rm: unsupported option %q\n", argument)), 1), nil
			}
			continue
		}
		if options && len(argument) > 1 && argument[0] == '-' {
			for _, option := range argument[1:] {
				switch option {
				case 'f':
					force = true
				case 'r', 'R':
					recursive = true
				default:
					return commandResult(shell, nil, []byte(fmt.Sprintf("rm: unsupported option -%c\n", option)), 1), nil
				}
			}
			continue
		}
		operands = append(operands, argument)
	}
	if len(operands) == 0 {
		return commandResult(shell, nil, []byte("rm: missing operand\n"), 1), nil
	}
	if _, unresolved := shell.Directory(); unresolved {
		for _, operand := range operands {
			if !path.IsAbs(operand) {
				return unresolvedCommandResult(shell), nil
			}
		}
	}

	var stderr []byte
	exitCode := 0
	for _, operand := range operands {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := shell.ResolvePath(operand)
		kind := shell.PathKind(name)
		if kind == runtime.PathMissing {
			if !force {
				stderr = append(stderr, fmt.Sprintf("rm: cannot remove %q: No such file or directory\n", operand)...)
				exitCode = 1
			}
			continue
		}
		if kind == runtime.PathDirectory && !recursive {
			stderr = append(stderr, fmt.Sprintf("rm: cannot remove %q: Is a directory\n", operand)...)
			exitCode = 1
			continue
		}
		if err := shell.RemovePath(ctx, name, recursive); err != nil {
			return nil, err
		}
	}
	return commandResult(shell, nil, stderr, exitCode), nil
}
