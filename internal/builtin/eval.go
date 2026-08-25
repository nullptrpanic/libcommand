package builtin

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerRestoreAlways("eval", executeEval)
}

func executeEval(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	return shell.Evaluate(strings.Join(args, " "), "eval", 1), nil
}
