package runtime

import (
	"fmt"
	"math"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/syntax"
)

func (e *ExecutionContext) invokeCommand(state *State, source *location, commandSyntax syntax.Command, definition *CommandDefinition, invocation *Invocation, argv0 *string, redirectionScope *redirectionPlan) (paths []*pathResult, err error) {
	var contextSyntax syntax.Command
	switch commandSyntax.(type) {
	case *syntax.DeclClause, *syntax.LetClause:
		contextSyntax = commandSyntax
	}
	commandContext := &CommandContext{
		execution:        e,
		state:            state,
		source:           source,
		syntax:           contextSyntax,
		argv0:            invocation.Name,
		redirects:        state.commandRedirects(e.redirects),
		redirectionScope: redirectionScope,
	}
	if commandSyntax != nil && len(state.redirectionFrames) != 0 {
		frame := state.redirectionFrames[len(state.redirectionFrames)-1]
		if frame.command == commandSyntax {
			commandContext.redirectionScope = frame
		}
	}
	if argv0 != nil {
		commandContext.argv0 = *argv0
	}
	prepared := definition.Prepare != nil && contextSyntax != nil
	if prepared {
		snapshot := state.clone()
		if prepareErr := definition.Prepare(commandContext, invocation); prepareErr != nil {
			*state = *snapshot
			if e.releaseExecutionStepOnSubstitution(prepareErr) {
				return nil, prepareErr
			}
			return e.pathsFromEvaluationError(state, prepareErr, source)
		}
		if commandContext.expansions != nil {
			commandContext.expansions.ready = true
		}
	}
	internal := definition.Builtin && !definition.UserOverride && !definition.Observed
	if prepared || !internal {
		additional := 0
		if commandContext.expansions != nil {
			additional = commandContext.expansions.bytes
		}
		if !internal {
			input, _ := state.stdin.Data()
			bytes, ok := commandInvocationMaterialization(state, invocation.Name, invocation.Args, input)
			if !ok {
				return e.pathsFromEvaluationError(state, materialize.LimitError(e.config.MaxMemoryBytes), source)
			}
			additional = addRetainedBytes(additional, bytes)
		} else {
			for _, argument := range invocation.Args {
				additional = addRetainedBytes(additional, materialize.EntryBytes+len(argument.Value))
			}
		}
		if err := e.checkPathsMaterialization([]*pathResult{{state: state, status: StatusCompleted}}, additional, source); err != nil {
			return []*pathResult{{state: state, status: StatusIncomplete}}, err
		}
	}
	populateCommandInvocation(state, invocation, internal)
	e.trace.commandStarted(e, state, commandSyntax, invocation)
	var result *CommandResult
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("execute command %q panicked: %v", invocation.Name, recovered)
			}
		}()
		result, err = definition.Command(e.ctx, commandContext, invocation)
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
	for _, path := range paths {
		// A delayed operation can return an already-observed inner status;
		// the enclosing command still has its own failure semantics.
		path.failureHandled = false
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
		return c.executeInvoke(operation.name, operation.arguments, operation.builtinOnly, operation.argv0)
	case commandOperationInvokeEnvironment:
		return c.executeInvokeWithEnvironment(operation.name, operation.arguments, operation.clearEnvironment, operation.unset, operation.assignments)
	case commandOperationReplace:
		return c.executeReplace(operation.name, operation.arguments, operation.clearEnvironment, operation.argv0)
	case commandOperationEvaluate:
		return c.executeEvaluate(operation.source, operation.name, operation.parseExitCode)
	case commandOperationSource:
		return c.executeSource(operation.source, operation.name, operation.arguments)
	case commandOperationRunShell:
		return c.executeRunShell(operation.program, operation.arguments)
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
