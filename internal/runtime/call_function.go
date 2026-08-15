package runtime

import (
	"strconv"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func (e *ExecutionContext) applyTemporaryAssignments(s *State, assignments []*syntax.Assign) (map[string]*savedVariable, error) {
	if len(assignments) == 0 {
		return nil, nil
	}
	saved := make(map[string]*savedVariable, len(assignments))
	for _, assignment := range assignments {
		if assignment.Name == nil {
			continue
		}
		name := assignment.Name.Value
		if _, exists := saved[name]; exists {
			continue
		}
		value, exists := s.vars.lookup(name)
		saved[name] = &savedVariable{value: value, exists: exists, unknown: s.vars.isUnknown(name), indexedSlots: s.vars.indexedSlots(name)}
	}
	if err := e.applyAssignments(s, assignments, expand.Unknown, true); err != nil {
		restoreTemporaryAssignments([]*pathResult{{state: s}}, saved)
		return nil, err
	}
	for name, variable := range saved {
		variable.appliedVersion = s.vars.version(name)
		saved[name] = variable
	}
	return saved, nil
}

func restoreTemporaryAssignments(paths []*pathResult, saved map[string]*savedVariable) {
	restoreSavedAssignments(paths, saved, false)
}

func restoreTemporaryAssignmentsAlways(paths []*pathResult, saved map[string]*savedVariable) {
	restoreSavedAssignments(paths, saved, true)
}

func restoreSavedAssignments(paths []*pathResult, saved map[string]*savedVariable, force bool) {
	visited := make(map[*State]struct{})
	var restore func(*State)
	restore = func(current *State) {
		if current == nil {
			return
		}
		if _, exists := visited[current]; exists {
			return
		}
		visited[current] = struct{}{}
		for name, variable := range saved {
			if !force && variable.appliedVersion != 0 && current.vars.version(name) != variable.appliedVersion {
				continue
			}
			if variable.exists {
				current.vars.putIndexedWithCertainty(name, variable.value, variable.unknown, variable.indexedSlots)
			} else {
				current.vars.delete(name)
			}
		}
		restore(current.exitFailure)
	}
	for _, path := range paths {
		restore(path.state)
	}
}

func (e *ExecutionContext) evaluateFunctionCallAfterStep(s *State, source syntax.Node, assignments []*syntax.Assign, declaration *syntax.FuncDecl, args []string) ([]*pathResult, error) {
	if status := e.checkContext(s, sourceLocation(source)); status != StatusCompleted {
		return []*pathResult{{state: s, status: status}}, nil
	}
	s.pushLocalScope()
	s.funcDepth++
	e.setFunctionArguments(s, args)
	for _, assignment := range assignments {
		if assignment.Name != nil {
			s.saveLocal(assignment.Name.Value)
		}
	}
	if err := e.applyAssignments(s, assignments, expand.Unknown, true); err != nil {
		s.funcDepth--
		s.popLocalScope()
		if e.releaseExecutionStepOnSubstitution(err) {
			return nil, err
		}
		if result, resultErr, incomplete := e.incompleteFromEvaluationError(s, err, sourceLocation(source)); incomplete {
			return []*pathResult{{state: s, status: result.status}}, resultErr
		}
		result := e.unresolved(s, err.Error(), sourceLocation(source))
		return []*pathResult{{state: s, status: result.status}}, nil
	}

	errCommand, inheritedErrTrap := s.traps["ERR"]
	inheritErrTrap := s.options.errTrace
	if !inheritErrTrap {
		s.deleteTrap("ERR")
	}
	paths, err := e.evaluateStatement(s, declaration.Body)
	for _, path := range paths {
		if !inheritErrTrap {
			if inheritedErrTrap {
				path.state.setTrap("ERR", errCommand)
			} else {
				path.state.deleteTrap("ERR")
			}
		}
		if path.state.signal == signalReturn {
			path.state.signal = signalNone
		}
		path.state.funcDepth--
		path.state.popLocalScope()
	}
	return paths, err
}

func (e *ExecutionContext) setFunctionArguments(s *State, args []string) {
	for _, name := range []string{"#", "@", "*"} {
		s.saveLocal(name)
		s.vars.delete(name)
	}
	for _, name := range s.vars.names() {
		if index, err := strconv.Atoi(name); err == nil && index > 0 {
			s.saveLocal(name)
			s.vars.delete(name)
		}
	}
	for index := range args {
		s.saveLocal(strconv.Itoa(index + 1))
	}
	s.replacePositionalArguments(args)
}
