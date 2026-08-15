package builtin

import (
	"context"
	"strconv"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerCommand("shift", executeShift)
}

func executeShift(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return shell.ResultUnknown(&runtime.CommandResult{ExitCode: 1}, false, true, false), nil
	}
	amount := 1
	if len(args) > 1 {
		return &runtime.CommandResult{Stderr: []byte("shift: too many arguments\n"), ExitCode: 1}, nil
	}
	if len(args) == 1 {
		value, err := strconv.Atoi(args[0])
		if err != nil || value < 0 {
			return &runtime.CommandResult{Stderr: []byte("shift: numeric argument required\n"), ExitCode: 2}, nil
		}
		amount = value
	}
	arguments := shell.PositionalArguments()
	if amount > len(arguments) {
		return &runtime.CommandResult{ExitCode: 1}, nil
	}
	shell.ReplacePositionalArguments(arguments[amount:])
	return &runtime.CommandResult{}, nil
}
