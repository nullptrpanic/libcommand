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
		return shell.UnresolvedResult(), nil
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
				return rmFailure(fmt.Sprintf("rm: unsupported option %q\n", argument)), nil
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
					return rmFailure(fmt.Sprintf("rm: unsupported option -%c\n", option)), nil
				}
			}
			continue
		}
		operands = append(operands, argument)
	}
	if len(operands) == 0 {
		return rmFailure("rm: missing operand\n"), nil
	}
	if _, unresolved := shell.Directory(); unresolved {
		for _, operand := range operands {
			if !path.IsAbs(operand) {
				return shell.UnresolvedResult(), nil
			}
		}
	}

	result := &runtime.CommandResult{}
	for _, operand := range operands {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := shell.ResolvePath(operand)
		kind := shell.PathKind(name)
		if kind == runtime.PathMissing {
			if !force {
				result.Stderr = append(result.Stderr, fmt.Sprintf("rm: cannot remove %q: No such file or directory\n", operand)...)
				result.ExitCode = 1
			}
			continue
		}
		if kind == runtime.PathDirectory && !recursive {
			result.Stderr = append(result.Stderr, fmt.Sprintf("rm: cannot remove %q: Is a directory\n", operand)...)
			result.ExitCode = 1
			continue
		}
		if err := shell.RemovePath(ctx, name, recursive); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func rmFailure(message string) *runtime.CommandResult {
	return &runtime.CommandResult{Stderr: []byte(message), ExitCode: 1}
}
