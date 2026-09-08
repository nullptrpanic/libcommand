package runtime

import (
	"errors"
	"fmt"
	"maps"

	"mvdan.cc/sh/v3/syntax"
)

func (e *ExecutionContext) evaluateBackground(s *State, statement *syntax.Stmt) ([]*pathResult, error) {
	releaseParent, err := e.retainNestedShellParent(s)
	if err != nil {
		return nil, err
	}
	defer releaseParent()
	child := s.clone()
	clearInheritedExitTrap(child)
	child.bindInput(newCertain[[]byte](nil))
	child.resetOutput()
	foreground := *statement
	foreground.Background = false
	previousBackgroundLimit := e.backgroundStepLimit
	e.backgroundStepLimit = e.nextBackgroundStepLimit()
	defer func() {
		e.backgroundStepLimit = previousBackgroundLimit
	}()
	childPaths, err := e.evaluateStatement(child, &foreground)
	childPaths, trapErr := e.evaluateExitTraps(childPaths)
	if err == nil {
		err = trapErr
	}
	results := make([]*pathResult, 0, len(childPaths))
	for _, childPath := range childPaths {
		parent := s.clone()
		inheritPathIdentity(parent, childPath.state)
		mergeIssue(parent, childPath.state)
		parent.fs = childPath.state.fs.clone()
		parent.setExitCode(0)
		status := childPath.status
		stdout, stdoutUnresolved := childPath.state.stdout.Data()
		stderr, stderrUnresolved := childPath.state.stderr.Data()
		if outputStatus := e.appendCapturedStreams(parent, stdout, stderr, stdoutUnresolved, stderrUnresolved, sourceLocation(statement)); outputStatus != StatusCompleted {
			status = outputStatus
		}
		if status == StatusCompleted {
			parent.backgroundPIDSet = true
		}
		results = append(results, &pathResult{state: parent, status: status})
	}
	return results, err
}

func (e *ExecutionContext) nextBackgroundStepLimit() int {
	maximum := e.config.MaxExecutionSteps
	if e.backgroundStepLimit > 0 && e.backgroundStepLimit < maximum {
		maximum = e.backgroundStepLimit
	}
	remaining := maximum - e.executedSteps
	if remaining <= 0 {
		return e.executedSteps
	}
	allowance := remaining / 2
	if allowance == 0 {
		allowance = 1
	}
	return e.executedSteps + allowance
}

func (e *ExecutionContext) backgroundLoopBudgetExhausted() bool {
	return e.backgroundStepLimit > 0 && e.executedSteps >= e.backgroundStepLimit
}

func (e *ExecutionContext) evaluateSubshell(s *State, subshell *syntax.Subshell) ([]*pathResult, error) {
	releaseParent, err := e.retainNestedShellParent(s)
	if err != nil {
		return nil, err
	}
	defer releaseParent()
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
		inheritPathIdentity(parent, childPath.state)
		mergeIssue(parent, childPath.state)
		mergeChildInput(parent, childPath.state)
		parent.fs = childPath.state.fs.clone()
		status := childPath.status
		stdout, stdoutUnresolved := childPath.state.stdout.Data()
		stderr, stderrUnresolved := childPath.state.stderr.Data()
		if outputStatus := e.appendCapturedStreams(parent, stdout, stderr, stdoutUnresolved, stderrUnresolved, sourceLocation(subshell)); outputStatus != StatusCompleted {
			status = outputStatus
		}
		exitCode, exitUnresolved := childPath.state.exitStatus.Data()
		parent.setExitStatus(exitCode, exitUnresolved)
		results = append(results, &pathResult{state: parent, status: status})
	}
	return results, err
}

func (e *ExecutionContext) evaluateSubstitutionPaths(s *State, substitution *syntax.CmdSubst) ([]*pathResult, error) {
	if result, ok, err := e.inputOnlySubstitution(s, substitution); ok || err != nil {
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
		s.setSubstitution(substitution, result)
		return []*pathResult{{state: s, status: StatusCompleted}}, nil
	}

	releaseParent, err := e.retainNestedShellParent(s)
	if err != nil {
		return nil, err
	}
	defer releaseParent()
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
	child.captureDescriptor(1)
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
		if outputStatus := e.appendCapturedStreams(parent, nil, stderr, false, stderrUnresolved, sourceLocation(substitution)); outputStatus != StatusCompleted {
			status = outputStatus
		}
		if status != StatusCompleted {
			results = append(results, &pathResult{state: parent, status: status})
			continue
		}
		stdout := trimUncertainBytes(childPath.state.stdout, "\n")
		parent.setSubstitution(substitution, &substitutionResult{
			stdout:     stdout,
			exitStatus: childPath.state.exitStatus,
		})
		results = append(results, &pathResult{state: parent, status: StatusCompleted})
	}
	return results, err
}

func substitutionParent(parentState, childState *State) *State {
	parent := parentState.clone()
	inheritPathIdentity(parent, childState)
	mergeIssue(parent, childState)
	mergeChildInput(parent, childState)
	parent.fs = childState.fs.clone()
	return parent
}

func inheritPathIdentity(target, source *State) {
	target.pathID = source.pathID
	target.parent = source.parent
	target.retainedParentBytes = source.retainedParentBytes
	target.frozen = false
	target.frozenBytes = 0
}

func mergeIssue(parent, child *State) {
	if child.issue != nil {
		parent.issue = child.issue
	}
}

func mergeChildInput(parent, child *State) {
	parentInput, childInput := parent.descriptors[0], child.descriptors[0]
	if parentInput == nil && childInput == nil || parentInput != nil && childInput != nil && parentInput.key == childInput.key {
		parent.stdin = child.stdin
	}
	inputs := make(map[*byte]*uncertain[[]byte])
	for _, descriptor := range child.descriptors {
		if descriptor.input != nil {
			inputs[descriptor.key] = descriptor.input
		}
	}
	updated := maps.Clone(parent.descriptors)
	for fd, descriptor := range updated {
		if input, exists := inputs[descriptor.key]; exists && descriptor.input != input {
			copy := *descriptor
			copy.input = input
			updated[fd] = &copy
		}
	}
	parent.setDescriptors(updated)
}
