package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	register("cat", executeCat)
}

func executeCat(ctx context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	arguments, concrete := concreteArguments(invocation)
	if !concrete {
		return unresolvedCommandResult(shell), nil
	}

	operands := make([]string, 0, len(arguments))
	options := true
	for _, argument := range arguments {
		if options && argument == "--" {
			options = false
			continue
		}
		if options && argument != "-" && strings.HasPrefix(argument, "-") {
			return commandResult(shell, nil, []byte(fmt.Sprintf("cat: unsupported option %q\n", argument)), 1), nil
		}
		operands = append(operands, argument)
	}
	if len(operands) == 0 {
		operands = append(operands, "-")
	}

	maximum := shell.MaxMemoryBytes()
	stdout := make([]byte, 0)
	var stderr []byte
	exitCode := 0
	stdoutUnknown := false
	stderrUnknown := false
	exitUnknown := false
	stdinConsumed := false
	for _, operand := range operands {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var contents []byte
		unknown := false
		if operand == "-" {
			if stdinConsumed {
				continue
			}
			stdinConsumed = true
			contents = invocation.Stdin
			unknown = invocation.Unresolved != nil && invocation.Unresolved.Stdin
		} else {
			name := shell.ResolvePath(operand)
			if shell.PathKind(name) == runtime.PathDirectory {
				diagnostic := fmt.Sprintf("cat: %s: Is a directory\n", operand)
				if _, ok := materialize.Add(len(stdout)+len(stderr), len(diagnostic), maximum); !ok {
					return nil, materialize.LimitError(maximum)
				}
				stderr = append(stderr, diagnostic...)
				exitCode = 1
				continue
			}
			var exists bool
			contents, unknown, exists = shell.ReadFile(name)
			if !exists {
				stdoutUnknown = true
				stderrUnknown = true
				exitUnknown = true
				continue
			}
		}

		if _, ok := materialize.Add(len(stdout)+len(stderr), len(contents), maximum); !ok {
			return nil, materialize.LimitError(maximum)
		}
		stdout = append(stdout, contents...)
		stdoutUnknown = stdoutUnknown || unknown
	}
	if stdinConsumed {
		shell.SetInput(nil, false)
	}

	return uncertainCommandResult(shell, stdout, stderr, exitCode, stdoutUnknown, stderrUnknown, exitUnknown && exitCode == 0), nil
}
