package analysis

import (
	"context"
	"path"

	"github.com/nullptrpanic/libcommand"
)

func commandResult(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation, riskType RiskType) (*libcommand.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if riskType != "" {
		return nil, &DetectionError{Command: invocation.Name, Type: riskType}
	}
	return shell.Result(shell.Output().
		Stdout(libcommand.Unresolved[[]byte](nil)).
		Stderr(libcommand.Unresolved[[]byte](nil)).
		ExitCode(libcommand.Unresolved(0)).
		Build()), nil
}

func argumentRiskResult(
	ctx context.Context,
	shell *libcommand.CommandContext,
	invocation *libcommand.Invocation,
	risky func([]string) bool,
	riskType RiskType,
) (*libcommand.CommandResult, error) {
	arguments, concrete := concreteArguments(invocation.Args)
	if !concrete || !risky(arguments) {
		riskType = ""
	}
	return commandResult(ctx, shell, invocation, riskType)
}

func concreteArguments(arguments []*libcommand.Argument) ([]string, bool) {
	values := make([]string, len(arguments))
	for index, argument := range arguments {
		if argument.Kind != libcommand.ArgumentString {
			return nil, false
		}
		values[index] = argument.Value
	}
	return values, true
}

func resolvedPath(directory, name string, directoryUnknown bool) (string, bool) {
	if path.IsAbs(name) {
		return path.Clean(name), true
	}
	if directoryUnknown {
		return "", false
	}
	return path.Join(directory, name), true
}

func directoryUnresolved(invocation *libcommand.Invocation) bool {
	return invocation.Unresolved != nil && invocation.Unresolved.Dir
}
