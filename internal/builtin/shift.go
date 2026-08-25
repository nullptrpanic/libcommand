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
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	amount := 1
	if len(args) > 1 {
		return commandResult(shell, nil, []byte("shift: too many arguments\n"), 1), nil
	}
	if len(args) == 1 {
		value, err := strconv.Atoi(args[0])
		if err != nil || value < 0 {
			return commandResult(shell, nil, []byte("shift: numeric argument required\n"), 2), nil
		}
		amount = value
	}
	arguments := shell.PositionalArguments()
	if amount > len(arguments) {
		return commandResult(shell, nil, nil, 1), nil
	}
	shell.ReplacePositionalArguments(arguments[amount:])
	return commandResult(shell, nil, nil, 0), nil
}
