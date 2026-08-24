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
		return shell.UnresolvedResult(), nil
	}

	operands := make([]string, 0, len(arguments))
	options := true
	for _, argument := range arguments {
		if options && argument == "--" {
			options = false
			continue
		}
		if options && argument != "-" && strings.HasPrefix(argument, "-") {
			return catFailure(fmt.Sprintf("cat: unsupported option %q\n", argument)), nil
		}
		operands = append(operands, argument)
	}
	if len(operands) == 0 {
		operands = append(operands, "-")
	}

	maximum := shell.MaxMemoryBytes()
	stdout := make([]byte, 0)
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
				if stdinConsumed {
					shell.SetInput(nil, false)
				}
				result := &runtime.CommandResult{
					Stdout:   stdout,
					Stderr:   []byte(fmt.Sprintf("cat: %s: Is a directory\n", operand)),
					ExitCode: 1,
				}
				return shell.ResultUnknown(result, stdoutUnknown, stderrUnknown, exitUnknown), nil
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

		if _, ok := materialize.Add(len(stdout), len(contents), maximum); !ok {
			return nil, materialize.LimitError(maximum)
		}
		stdout = append(stdout, contents...)
		stdoutUnknown = stdoutUnknown || unknown
	}
	if stdinConsumed {
		shell.SetInput(nil, false)
	}

	return shell.ResultUnknown(&runtime.CommandResult{Stdout: stdout}, stdoutUnknown, stderrUnknown, exitUnknown), nil
}

func catFailure(message string) *runtime.CommandResult {
	return &runtime.CommandResult{Stderr: []byte(message), ExitCode: 1}
}
