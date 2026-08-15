package runtime

import (
	"fmt"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func (e *ExecutionContext) evaluateCallStatement(s *State, call *syntax.CallExpr) ([]*pathResult, error) {
	if len(call.Args) == 0 {
		return e.evaluateAssignmentCall(s, call)
	}
	if status := e.reserveExecutionSteps(s, 1, sourceLocation(call)); status != StatusCompleted {
		return []*pathResult{{state: s, status: status}}, nil
	}
	arguments, err := e.expandCallArguments(s, call.Args)
	if err != nil {
		if e.releaseExecutionStepOnSubstitution(err) {
			return nil, err
		}
		result, resultErr := e.outcomeFromExpansionError(s, err, fmt.Sprintf("expand command: %v", err), sourceLocation(call))
		return []*pathResult{{state: s, status: result.status}}, resultErr
	}
	if len(arguments) == 0 || arguments[0].Kind == ArgumentUnresolved {
		return e.unresolvedPath(s, "command name depends on unresolved command output", sourceLocation(call.Args[0])), nil
	}
	if arguments[0].Value == "" {
		return e.unresolvedPath(s, "command name expanded to an empty value", sourceLocation(call)), nil
	}
	return e.evaluateExpandedCommand(s, call, call, arguments[0].Value, arguments[1:], call)
}

func (e *ExecutionContext) evaluateExpandedCommand(s *State, source syntax.Node, call *syntax.CallExpr, name string, arguments []*Argument, commandSyntax syntax.Command) ([]*pathResult, error) {
	assignments := []*syntax.Assign(nil)
	if call != nil {
		assignments = call.Assigns
	}
	if function := s.functions[name]; function != nil {
		if hasUnresolvedArguments(arguments) {
			if call != nil {
				for _, word := range call.Args[1:] {
					if wordHasHostUnknown(s, word) {
						return e.unresolvedPath(s, "function argument depends on host runtime state", sourceLocation(source)), nil
					}
				}
			}
			return e.unresolvedPath(s, "function argument depends on unresolved command output", sourceLocation(source)), nil
		}
		return e.evaluateFunctionCallAfterStep(s, source, assignments, function, concreteArgumentValues(arguments))
	}
	definition := e.lookupCommandDefinition(name)
	if !commandDefinitionExecutable(definition) {
		s.setUnknownExitCode()
		status := e.appendStreams(s, nil, nil, true, true, sourceLocation(source))
		return []*pathResult{{state: s, status: status}}, nil
	}

	saved, assignmentErr := e.applyTemporaryAssignments(s, assignments)
	if assignmentErr != nil {
		if e.releaseExecutionStepOnSubstitution(assignmentErr) {
			return nil, assignmentErr
		}
		if result, resultErr, incomplete := e.incompleteFromEvaluationError(s, assignmentErr, sourceLocation(source)); incomplete {
			return []*pathResult{{state: s, status: result.status}}, resultErr
		}
		return e.unresolvedPath(s, assignmentErr.Error(), sourceLocation(source)), nil
	}
	input, _ := s.stdin.Data()
	invocation, status, invocationErr := e.commandInvocation(
		s,
		name,
		arguments,
		input,
		sourceLocation(source),
		definition.Builtin && !definition.UserOverride,
	)
	if invocationErr != nil || status != StatusCompleted {
		paths := []*pathResult{{state: s, status: status}}
		restoreTemporaryAssignments(paths, saved)
		return paths, invocationErr
	}
	paths, commandErr := e.invokeCommand(s, sourceLocation(source), commandSyntax, definition.Command, invocation)
	if definition.RestoreAssignments {
		restoreTemporaryAssignmentsAlways(paths, saved)
	} else {
		restoreTemporaryAssignments(paths, saved)
	}
	return paths, commandErr
}

func (e *ExecutionContext) evaluateAssignmentCall(s *State, call *syntax.CallExpr) ([]*pathResult, error) {
	if status := e.reserveExecutionSteps(s, 1, sourceLocation(call)); status != StatusCompleted {
		return []*pathResult{{state: s, status: status}}, nil
	}
	if err := e.applyAssignments(s, call.Assigns, expand.Unknown, false); err != nil {
		if e.releaseExecutionStepOnSubstitution(err) {
			return nil, err
		}
		if result, resultErr, incomplete := e.incompleteFromEvaluationError(s, err, sourceLocation(call)); incomplete {
			return []*pathResult{{state: s, status: result.status}}, resultErr
		}
		return e.unresolvedPath(s, err.Error(), sourceLocation(call)), nil
	}
	if exitCode, exitUnknown, exists := s.lastSubstitutionExitStatus(); exists {
		s.setExitStatus(exitCode, exitUnknown)
	} else {
		s.setExitCode(0)
	}
	return []*pathResult{{state: s, status: StatusCompleted}}, nil
}
