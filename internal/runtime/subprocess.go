package runtime

import (
	"errors"
	"fmt"

	"mvdan.cc/sh/v3/syntax"
)

func (e *ExecutionContext) evaluateBackground(s *State, statement *syntax.Stmt) ([]*pathResult, error) {
	child := s.clone()
	clearInheritedExitTrap(child)
	child.stdin = newCertain[[]byte](nil)
	child.resetOutput()
	foreground := *statement
	foreground.Background = false
	childPaths, err := e.evaluateStatement(child, &foreground)
	if err == nil {
		childPaths, err = e.resolveCorrelatedExitStatuses(childPaths, sourceLocation(statement))
	}
	childPaths, trapErr := e.evaluateExitTraps(childPaths)
	if err == nil {
		err = trapErr
	}
	results := make([]*pathResult, 0, len(childPaths))
	for _, childPath := range childPaths {
		parent := s.clone()
		mergeIssue(parent, childPath.state)
		parent.fs = childPath.state.fs.clone()
		parent.setExitCode(0)
		status := childPath.status
		stdout, stdoutUnresolved := childPath.state.stdout.Data()
		stderr, stderrUnresolved := childPath.state.stderr.Data()
		if outputStatus := e.appendStreams(parent, stdout, stderr, stdoutUnresolved, stderrUnresolved, sourceLocation(statement)); outputStatus != StatusCompleted {
			status = outputStatus
		}
		if status == StatusCompleted {
			parent.backgroundPIDSet = true
		}
		results = append(results, &pathResult{state: parent, status: status})
	}
	return results, err
}

func (e *ExecutionContext) evaluateSubshell(s *State, subshell *syntax.Subshell) ([]*pathResult, error) {
	child := s.clone()
	clearInheritedExitTrap(child)
	if !child.options.errTrace {
		child.deleteTrap("ERR")
	}
	child.resetOutput()
	childPaths, err := e.evaluateStatements([]*pathResult{{state: child, status: StatusCompleted}}, subshell.Stmts)
	childPaths, trapErr := e.evaluateExitTraps(childPaths)
	if err == nil {
		err = trapErr
	}
	results := make([]*pathResult, 0, len(childPaths))
	for _, childPath := range childPaths {
		parent := s.clone()
		mergeIssue(parent, childPath.state)
		mergeChildInput(parent, childPath.state)
		parent.fs = childPath.state.fs.clone()
		status := childPath.status
		stdout, stdoutUnresolved := childPath.state.stdout.Data()
		stderr, stderrUnresolved := childPath.state.stderr.Data()
		if outputStatus := e.appendStreams(parent, stdout, stderr, stdoutUnresolved, stderrUnresolved, sourceLocation(subshell)); outputStatus != StatusCompleted {
			status = outputStatus
		}
		exitCode, exitUnresolved := childPath.state.exitStatus.Data()
		parent.setExitStatus(exitCode, exitUnresolved)
		results = append(results, &pathResult{state: parent, status: status})
	}
	return results, err
}

func (e *ExecutionContext) evaluateSubstitutionPaths(s *State, substitution *syntax.CmdSubst) ([]*pathResult, error) {
	if result, failures, ok, err := e.inputOnlySubstitution(s, substitution); ok || err != nil {
		if err != nil {
			var failure *redirectionFailure
			if errors.As(err, &failure) {
				if status := e.checkContext(s, sourceLocation(substitution)); status != StatusCompleted {
					return []*pathResult{{state: s, status: status}}, nil
				}
				s.setSubstitution(substitution, &substitutionResult{stdout: newCertain[[]byte](nil), exitStatus: newCertain(1)})
				status := e.appendStreams(s, nil, []byte(failure.Error()+"\n"), false, false, sourceLocation(substitution))
				return []*pathResult{{state: s, status: status}}, nil
			}
			return e.unresolvedPath(s, fmt.Sprintf("evaluate input substitution: %v", err), sourceLocation(substitution)), nil
		}
		if status := e.checkContext(s, sourceLocation(substitution)); status != StatusCompleted {
			return []*pathResult{{state: s, status: status}}, nil
		}
		result.stdout = trimUncertainBytes(result.stdout, "\n")
		return e.setSubstitutionAlternatives(s, substitution, result, failures)
	}

	child := s.clone()
	clearInheritedExitTrap(child)
	if !child.options.inheritErrexit {
		child.options.errexit = false
	}
	if !child.options.errTrace {
		child.deleteTrap("ERR")
	}
	if status := e.checkContext(child, sourceLocation(substitution)); status != StatusCompleted {
		return []*pathResult{{state: child, status: status}}, nil
	}
	child.resetOutput()
	childPaths, err := e.evaluateStatements([]*pathResult{{state: child, status: StatusCompleted}}, substitution.Stmts)
	childPaths, trapErr := e.evaluateExitTraps(childPaths)
	if err == nil {
		err = trapErr
	}
	results := make([]*pathResult, 0, len(childPaths))
	for _, childPath := range childPaths {
		parent := substitutionParent(s, childPath.state)
		status := childPath.status
		stderr, stderrUnresolved := childPath.state.stderr.Data()
		if outputStatus := e.appendStreams(parent, nil, stderr, false, stderrUnresolved, sourceLocation(substitution)); outputStatus != StatusCompleted {
			status = outputStatus
		}
		if status != StatusCompleted {
			results = append(results, &pathResult{state: parent, status: status})
			continue
		}
		stdout := trimUncertainBytes(childPath.state.stdout, "\n")
		alternatives, alternativeErr := e.setSubstitutionAlternatives(parent, substitution, &substitutionResult{
			stdout:     stdout,
			exitStatus: childPath.state.exitStatus,
		}, substitutionFailureState(s, childPath.state.exitFailure))
		results = append(results, alternatives...)
		if alternativeErr != nil {
			return results, alternativeErr
		}
	}
	return results, err
}

func (e *ExecutionContext) setSubstitutionAlternatives(s *State, substitution *syntax.CmdSubst, result *substitutionResult, failure *substitutionFailure) ([]*pathResult, error) {
	_, exitUnresolved := result.exitStatus.Data()
	if !exitUnresolved || failure == nil {
		s.setSubstitution(substitution, result)
		return []*pathResult{{state: s, status: StatusCompleted}}, nil
	}
	if status := e.reserveExecutionSteps(s, 1, sourceLocation(substitution)); status != StatusCompleted {
		return []*pathResult{{state: s, status: status}}, nil
	}

	paths := make([]*pathResult, 0, 2)
	s.setSubstitution(substitution, &substitutionResult{
		stdout:     result.stdout,
		exitStatus: newCertain(0),
	})
	paths = append(paths, &pathResult{state: s, status: StatusCompleted})
	failureState := failure.state.clone()
	failureState.setSubstitution(substitution, &substitutionResult{
		stdout:     failure.stdout,
		exitStatus: newCertain(1),
	})
	paths = append(paths, &pathResult{state: failureState, status: StatusCompleted})
	if err := e.checkPathsMaterialization(paths, 0, sourceLocation(substitution)); err != nil {
		return paths, err
	}
	return paths, nil
}

func substitutionParent(parentState, childState *State) *State {
	parent := parentState.clone()
	mergeIssue(parent, childState)
	mergeChildInput(parent, childState)
	parent.fs = childState.fs.clone()
	return parent
}

func substitutionFailureState(parent, childFailure *State) *substitutionFailure {
	if childFailure == nil {
		return nil
	}
	return &substitutionFailure{
		state:  substitutionParent(parent, childFailure),
		stdout: trimUncertainBytes(childFailure.stdout, "\n"),
	}
}

func mergeIssue(parent, child *State) {
	if child.issue != nil {
		parent.issue = child.issue
	}
}

func mergeChildInput(parent, child *State) {
	parent.stdin = child.stdin
}
