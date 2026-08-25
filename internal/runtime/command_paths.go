package runtime

import (
	"fmt"
	"math"

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
	applyResult := err == nil
	if applyResult && result != nil && result.operation == nil && len(result.outputs) > 1 {
		if status := e.reserveExecutionSteps(state, len(result.outputs)-1, source); status != StatusCompleted {
			paths = []*pathResult{{state: state, status: status}}
			applyResult = false
		}
	}
	if applyResult {
		err = validateCommandResultMaterialization(result, e.config.MaxMemoryBytes)
		applyResult = err == nil
	}
	if applyResult {
		paths, err = commandContext.applyResult(result)
	}
	commandContext.restoreUser(paths)
	releaseCommandResultStates(result)
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

func releaseCommandResultStates(result *CommandResult) {
	if result == nil {
		return
	}
	for _, output := range result.outputs {
		output.state = nil
	}
}

func (c *CommandContext) applyResult(result *CommandResult) ([]*pathResult, error) {
	if result == nil || result.operation == nil && len(result.outputs) == 0 {
		action := CommandContinue
		if result != nil {
			action = result.Action
		}
		result = c.Result(c.Output().
			Stdout(Unresolved[[]byte](nil)).
			Stderr(Unresolved[[]byte](nil)).
			ExitCode(Unresolved(0)).
			Build())
		result.Action = action
	}
	if result.operation != nil {
		return c.executeOperation(result.operation)
	}
	return c.applyOutputs(result), nil
}

func (c *CommandContext) applyOutputs(result *CommandResult) []*pathResult {
	status := StatusCompleted
	if result.Action == CommandStop {
		c.execution.stop = true
		status = StatusTerminated
	}
	paths := make([]*pathResult, 0, len(result.outputs))
	for _, output := range result.outputs {
		state := output.state
		output.state = nil
		stdout, stderr, exitCode, stdoutUnresolved, stderrUnresolved, exitUnresolved := output.values()
		state.setExitStatus(exitCode, exitUnresolved)
		pathStatus := c.execution.appendOutcome(state, &outcome{
			stdout:        stdout,
			stderr:        stderr,
			stdoutUnknown: stdoutUnresolved,
			stderrUnknown: stderrUnresolved,
			status:        status,
		}, c.source)
		paths = append(paths, &pathResult{state: state, status: pathStatus})
	}
	return paths
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
	total := 0
	stateBytes := 0
	baseline := 0
	baselineSet := false
	ok := true
	for _, output := range result.outputs {
		total, ok = materialize.Add(total, materialize.EntryBytes, maximum)
		if !ok {
			break
		}
		stdout, _ := output.Stdout.Data()
		stderr, _ := output.Stderr.Data()
		total, ok = materialize.Add(total, len(stdout), maximum)
		if ok {
			total, ok = materialize.Add(total, len(stderr), maximum)
		}
		if ok {
			var bytes int
			bytes, ok = stateMaterialization(output.state)
			if ok {
				stateBytes, ok = materialize.Add(stateBytes, bytes, math.MaxInt)
			}
			if !baselineSet && output.state != nil {
				baseline = output.state.initialBytes
				baselineSet = true
			}
		}
		if !ok {
			break
		}
	}
	if stateBytes > baseline {
		stateBytes -= baseline
	} else {
		stateBytes = 0
	}
	if ok {
		_, ok = materialize.Add(total, stateBytes, maximum)
	}
	if !ok {
		return materialize.LimitError(maximum)
	}
	return nil
}
