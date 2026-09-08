package builtin

import (
	"context"
	"fmt"

	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/syntax"
)

func init() {
	registerCommand("let", executeLet)
	definitions["let"].Prepare = func(shell *runtime.CommandContext, invocation *runtime.Invocation) error {
		clause, ok := shell.CommandSyntax().(*syntax.LetClause)
		if !ok {
			return nil
		}
		arguments, err := shell.PrepareLetArguments(clause)
		invocation.Args = arguments
		return err
	}
}

func executeLet(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	var expressions []syntax.ArithmExpr
	if clause, ok := shell.CommandSyntax().(*syntax.LetClause); ok && invocation.Args == nil {
		expressions = clause.Exprs
	} else {
		args, concrete := concreteArguments(invocation)
		if !concrete {
			return unresolvedStderrCommandResult(shell, 1), nil
		}
		expressions = make([]syntax.ArithmExpr, 0, len(args))
		for _, argument := range args {
			expression, err := shell.ParseArithmetic(argument)
			if err != nil {
				return commandResult(shell, nil, []byte(fmt.Sprintf("let: %v\n", err)), 1), nil
			}
			expressions = append(expressions, expression)
		}
	}
	return executeLetExpressions(shell, expressions)
}

func executeLetExpressions(shell *runtime.CommandContext, expressions []syntax.ArithmExpr) (*runtime.CommandResult, error) {
	snapshot := shell.Snapshot()
	value := 0
	for _, expression := range expressions {
		result, err := shell.Arithmetic(expression)
		if err != nil {
			shell.Restore(snapshot)
			return shell.ExpansionError(err, fmt.Sprintf("evaluate let expression: %v", err)), nil
		}
		if result.Unknown {
			shell.Restore(snapshot)
			return unresolvedStderrCommandResult(shell, 1), nil
		}
		if result.Failure != "" {
			return commandResult(shell, nil, []byte(fmt.Sprintf("let: %s\n", result.Failure)), 1), nil
		}
		value = result.Value
	}
	exitCode := 1
	if value != 0 {
		exitCode = 0
	}
	return commandResult(shell, nil, nil, exitCode), nil
}
