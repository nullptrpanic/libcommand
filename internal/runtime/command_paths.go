package runtime

import (
	"fmt"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/syntax"
)

func (e *ExecutionContext) invokeCommand(state *State, source *location, commandSyntax syntax.Command, command Command, invocation *Invocation) (paths []*pathResult, err error) {
	var contextSyntax syntax.Command
	switch commandSyntax.(type) {
	case *syntax.DeclClause, *syntax.LetClause:
		contextSyntax = commandSyntax
	}
	commandContext := &CommandContext{
		execution: e,
		state:     state,
		source:    source,
		syntax:    contextSyntax,
		redirects: cloneRedirects(e.redirects),
	}
	e.trace.commandStarted(e, state, commandSyntax, invocation)
	var result *CommandResult
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("execute command %q panicked: %v", invocation.Name, recovered)
			}
		}()
		result, err = command(e.ctx, commandContext, invocation)
	}()
	if err == nil {
		err = validateCommandResultMaterialization(result, e.config.MaxMemoryBytes)
	}
	if err == nil {
		paths, err = commandContext.applyResult(result)
	}
	commandContext.restoreUser(paths)
	e.trace.commandFinished(e, state, commandSyntax, result, err)
	if err != nil {
		if expansionRequested(err) {
			return paths, err
		}
		e.setIssue(state, err, source)
		return []*pathResult{{state: state, status: StatusIncomplete}}, err
	}
	return paths, nil
}

func (c *CommandContext) applyResult(result *CommandResult) ([]*pathResult, error) {
	if result != nil && result.operation != nil {
		return c.executeOperation(result.operation)
	}
	return c.applyOrdinaryResult(result)
}

func (c *CommandContext) applyOrdinaryResult(result *CommandResult) ([]*pathResult, error) {
	stdoutUnknown := false
	stderrUnknown := false
	exitUnknown := false
	preserveExit := false
	if result != nil {
		stdoutUnknown = result.stdoutUnknown
		stderrUnknown = result.stderrUnknown
		exitUnknown = result.exitUnknown
		preserveExit = result.preserveExit
	}
	if result == nil || result.Unresolved {
		result = &CommandResult{}
		stdoutUnknown = true
		stderrUnknown = true
		exitUnknown = true
	}
	if !preserveExit {
		if exitUnknown {
			c.state.setUnknownExitCode()
		} else {
			c.state.setExitCode(result.ExitCode)
		}
	}
	status := StatusCompleted
	if result.Action == CommandStop {
		c.execution.stop = true
		status = StatusTerminated
	}
	status = c.execution.appendOutcome(c.state, &outcome{
		stdout:        result.Stdout,
		stderr:        result.Stderr,
		stdoutUnknown: stdoutUnknown,
		stderrUnknown: stderrUnknown,
		status:        status,
	}, c.source)
	return []*pathResult{{state: c.state, status: status}}, nil
}

func (c *CommandContext) executeOperation(operation *commandOperation) ([]*pathResult, error) {
	switch operation.kind {
	case commandOperationUnresolved:
		return c.execution.unresolvedPath(c.state, operation.message, c.source), nil
	case commandOperationExpansionError:
		result, err := c.execution.outcomeFromExpansionError(c.state, operation.err, operation.message, c.source)
		status := c.execution.appendOutcome(c.state, result, c.source)
		return []*pathResult{{state: c.state, status: status}}, err
	case commandOperationInvoke:
		return c.executeInvoke(operation.name, operation.arguments, operation.builtinOnly)
	case commandOperationInvokeEnvironment:
		return c.executeInvokeWithEnvironment(operation.name, operation.arguments, operation.clearEnvironment, operation.unset, operation.assignments)
	case commandOperationReplace:
		return c.executeReplace(operation.name, operation.arguments, operation.clearEnvironment)
	case commandOperationEvaluate:
		return c.executeEvaluate(operation.source, operation.name, operation.parseExitCode)
	case commandOperationSource:
		return c.executeSource(operation.source, operation.name, operation.sourceArguments)
	case commandOperationRunShell:
		return c.executeRunShell(operation.program)
	default:
		return nil, fmt.Errorf("unsupported command operation %d", operation.kind)
	}
}

func validateCommandResultMaterialization(result *CommandResult, maximum int) error {
	if result == nil {
		return nil
	}
	maximum = normalizedMaxMemoryBytes(maximum)
	total, ok := materialize.Add(0, len(result.Stdout), maximum)
	if ok {
		_, ok = materialize.Add(total, len(result.Stderr), maximum)
	}
	if !ok {
		return materialize.LimitError(maximum)
	}
	return nil
}
