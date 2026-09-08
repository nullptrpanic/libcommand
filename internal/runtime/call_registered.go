package runtime

import (
	"fmt"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func (e *ExecutionContext) evaluateCallStatement(s *State, call *syntax.CallExpr) ([]*pathResult, error) {
	if status := e.reserveExecutionSteps(s, 1, sourceLocation(call)); status != StatusCompleted {
		return []*pathResult{{state: s, status: status}}, nil
	}
	if len(call.Args) == 0 {
		return e.evaluateAssignmentCall(s, call)
	}
	arguments, err := e.expandCallArguments(s, call.Args)
	if err != nil {
		if e.releaseExecutionStepOnSubstitution(err) {
			return nil, err
		}
		result, resultErr := e.outcomeFromExpansionError(s, err, fmt.Sprintf("expand command: %v", err), sourceLocation(call))
		return []*pathResult{{state: s, status: result.status}}, resultErr
	}
	if len(arguments) == 0 {
		return e.evaluateAssignmentCall(s, call)
	}
	if arguments[0].Kind == ArgumentUnresolved {
		return e.evaluateExpandedCommand(s, call, call, "", arguments[1:], call)
	}
	if arguments[0].Value == "" {
		saved, err := e.applyTemporaryAssignments(s, call.Assigns)
		if err != nil {
			return e.pathsFromEvaluationError(s, err, sourceLocation(call))
		}
		s.setExitCode(127)
		status := e.appendStreams(s, nil, []byte(": command not found\n"), false, false, sourceLocation(call))
		paths := []*pathResult{{state: s, status: status}}
		restoreTemporaryAssignments(paths, saved)
		return paths, nil
	}
	return e.evaluateExpandedCommand(s, call, call, arguments[0].Value, arguments[1:], call)
}

func (e *ExecutionContext) evaluateExpandedCommand(s *State, source syntax.Node, call *syntax.CallExpr, name string, arguments []*Argument, commandSyntax syntax.Command) ([]*pathResult, error) {
	assignments := []*syntax.Assign(nil)
	if call != nil {
		assignments = call.Assigns
	}
	if function := s.functions[name]; function != nil {
		return e.evaluateFunctionCallAfterStep(s, source, assignments, function, arguments)
	}
	definition := e.lookupCommandDefinition(name)
	if !commandDefinitionExecutable(definition) {
		s.setUnknownExitCode()
		status := e.appendStreams(s, nil, nil, true, true, sourceLocation(source))
		return []*pathResult{{state: s, status: status}}, nil
	}

	saved, assignmentErr := e.applyTemporaryAssignments(s, assignments)
	if assignmentErr != nil {
		return e.pathsFromEvaluationError(s, assignmentErr, sourceLocation(source))
	}
	invocation := &Invocation{Name: name, Args: arguments}
	paths, commandErr := e.invokeCommand(s, sourceLocation(source), commandSyntax, definition, invocation, nil, nil)
	if definition.RestoreAssignments {
		restoreTemporaryAssignmentsAlways(paths, saved)
	} else {
		restoreTemporaryAssignments(paths, saved)
	}
	return paths, commandErr
}

func (e *ExecutionContext) evaluateAssignmentCall(s *State, call *syntax.CallExpr) ([]*pathResult, error) {
	if err := e.applyAssignments(s, call.Assigns, expand.Unknown, false); err != nil {
		return e.pathsFromEvaluationError(s, err, sourceLocation(call))
	}
	if exitCode, exitUnknown, exists := s.lastSubstitutionExitStatus(); exists {
		s.setExitStatus(exitCode, exitUnknown)
	} else {
		s.setExitCode(0)
	}
	return []*pathResult{{state: s, status: StatusCompleted}}, nil
}
