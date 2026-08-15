package runtime

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func (e *ExecutionContext) evaluateWhile(s *State, clause *syntax.WhileClause) (results []*pathResult, err error) {
	s.pushLoop()
	defer func() {
		for _, result := range results {
			result.state.popLoop()
		}
	}()
	active := []*pathResult{{state: s, status: StatusCompleted}}
	completed := make([]*pathResult, 0, 1)
	for len(active) > 0 {
		conditionPaths, err := e.evaluateStatementsWithoutErrexit(active, clause.Cond)
		if err != nil {
			return append(completed, conditionPaths...), err
		}
		bodyPaths := make([]*pathResult, 0, len(conditionPaths))
		exitPaths := make([]*pathResult, 0, len(conditionPaths))
		for _, conditionPath := range conditionPaths {
			if conditionPath.status != StatusCompleted {
				exitPaths = append(exitPaths, conditionPath)
				continue
			}
			resolvedPaths, resolveErr := e.resolveExitStatus(conditionPath, sourceLocation(clause))
			if resolveErr != nil {
				return append(completed, resolvedPaths...), resolveErr
			}
			for _, resolved := range resolvedPaths {
				exitCode, _ := resolved.state.exitStatus.Data()
				matched := exitCode == 0
				if clause.Until {
					matched = !matched
				}
				if matched {
					bodyPaths = append(bodyPaths, resolved)
				} else {
					exitCode, exitUnknown := resolved.state.currentLoopExitStatus()
					resolved.state.setExitStatus(exitCode, exitUnknown)
					exitPaths = append(exitPaths, resolved)
				}
			}
		}
		completed = append(completed, exitPaths...)
		if err := e.checkPathGroupsMaterialization(0, sourceLocation(clause), completed, bodyPaths); err != nil {
			return append(completed, bodyPaths...), err
		}
		if len(bodyPaths) == 0 {
			break
		}
		bodyResults, err := e.evaluateStatements(bodyPaths, clause.Do)
		if err != nil {
			return append(completed, bodyResults...), err
		}
		active = active[:0]
		for _, bodyPath := range bodyResults {
			if bodyPath.status == StatusCompleted {
				exitCode, exitUnresolved := bodyPath.state.exitStatus.Data()
				bodyPath.state.setLoopExitStatus(exitCode, exitUnresolved)
			}
			e.collectLoopContinuation(&active, &completed, bodyPath)
		}
		if err := e.checkPathGroupsMaterialization(0, sourceLocation(clause), completed, active); err != nil {
			return append(completed, active...), err
		}
	}
	return completed, nil
}

func (e *ExecutionContext) evaluateFor(s *State, clause *syntax.ForClause) (results []*pathResult, err error) {
	s.loopDepth++
	defer func() {
		for _, result := range results {
			result.state.loopDepth--
		}
	}()

	switch loop := clause.Loop.(type) {
	case *syntax.WordIter:
		return e.evaluateWordFor(s, clause, loop)
	case *syntax.CStyleLoop:
		return e.evaluateArithmeticFor(s, clause, loop)
	default:
		result := e.unresolved(s, fmt.Sprintf("unsupported for loop %T", clause.Loop), sourceLocation(clause))
		return []*pathResult{{state: s, status: result.status}}, nil
	}
}

func (e *ExecutionContext) evaluateWordFor(s *State, clause *syntax.ForClause, loop *syntax.WordIter) ([]*pathResult, error) {
	items, expandErr := e.expandWords(s, loop.Items)
	if expandErr != nil {
		if expansionRequested(expandErr) {
			return nil, expandErr
		}
		if result, resultErr, incomplete := e.incompleteFromEvaluationError(s, expandErr, sourceLocation(loop)); incomplete {
			return []*pathResult{{state: s, status: result.status}}, resultErr
		}
		result := e.unresolved(s, fmt.Sprintf("expand for items: %v", expandErr), sourceLocation(loop))
		return []*pathResult{{state: s, status: result.status}}, nil
	}
	unknownItems := false
	if _, unknown := firstUnknownWord(s, loop.Items); unknown {
		if clause.Select || !e.candidates.contains(clause) {
			result := e.unresolved(s, "for item list depends on unresolved command output", sourceLocation(loop))
			return []*pathResult{{state: s, status: result.status}}, nil
		}
		if status := e.reserveExecutionSteps(s, 1, sourceLocation(loop)); status != StatusCompleted {
			return []*pathResult{{state: s, status: status}}, nil
		}
		items = []string{""}
		unknownItems = true
	}
	if s.vars.Get(loop.Name.Value).ReadOnly {
		s.setExitCode(1)
		status := e.appendStreams(s, nil, []byte(fmt.Sprintf("%s: readonly variable\n", loop.Name.Value)), false, false, sourceLocation(loop))
		return []*pathResult{{state: s, status: status}}, nil
	}
	if clause.Select {
		return e.evaluateSelect(s, clause, loop, items)
	}
	s.setExitCode(0)
	active := []*pathResult{{state: s, status: StatusCompleted}}
	completed := make([]*pathResult, 0)
	for _, item := range items {
		bodyInputs := make([]*pathResult, 0, len(active))
		for _, current := range active {
			if e.collectInactivePath(&completed, current) {
				continue
			}
			previous := current.state.vars.Get(loop.Name.Value)
			value := expand.Variable{
				Set:      true,
				Exported: previous.Exported,
				Kind:     expand.String,
				Str:      item,
			}
			if unknownItems {
				current.state.vars.putUnknown(loop.Name.Value, value)
			} else {
				current.state.vars.put(loop.Name.Value, value)
			}
			bodyInputs = append(bodyInputs, current)
		}
		if err := e.checkPathGroupsMaterialization(0, sourceLocation(clause), completed, bodyInputs); err != nil {
			return append(completed, bodyInputs...), err
		}
		bodyResults, bodyErr := e.evaluateStatements(bodyInputs, clause.Do)
		if bodyErr != nil {
			return append(completed, bodyResults...), bodyErr
		}
		active = active[:0]
		for _, bodyPath := range bodyResults {
			e.collectLoopContinuation(&active, &completed, bodyPath)
		}
		if err := e.checkPathGroupsMaterialization(0, sourceLocation(clause), completed, active); err != nil {
			return append(completed, active...), err
		}
	}
	return append(completed, active...), nil
}

func (e *ExecutionContext) evaluateArithmeticFor(s *State, clause *syntax.ForClause, loop *syntax.CStyleLoop) ([]*pathResult, error) {
	initial := []*pathResult{{state: s, status: StatusCompleted}}
	if loop.Init != nil {
		var hostUnknown bool
		var expandErr error
		initial, hostUnknown, expandErr = e.evaluateArithmeticEffect(s, loop.Init, "for initializer")
		if expandErr != nil {
			return initial, expandErr
		}
		if hostUnknown {
			return initial, nil
		}
	}
	for _, path := range initial {
		if path.status == StatusCompleted {
			path.state.setExitCode(0)
		}
	}
	active := initial
	completed := make([]*pathResult, 0)
	for len(active) > 0 {
		bodyInputs := make([]*pathResult, 0, len(active))
		for _, current := range active {
			if e.collectInactivePath(&completed, current) {
				continue
			}
			if status := e.reserveExecutionSteps(current.state, 1, sourceLocation(loop)); status != StatusCompleted {
				current.status = status
				completed = append(completed, current)
				continue
			}
			if loop.Cond == nil {
				bodyInputs = append(bodyInputs, current)
				continue
			}
			certainty := arithmeticCertainty(current.state, loop.Cond)
			hostUnknown := certainty.hostUnknown()
			dataUnknown := certainty.dataUnknown()
			originalVars := current.state.vars
			values, expandErr := e.evaluateArithmeticExpression(current.state, loop.Cond)
			if expandErr != nil {
				return append(completed, arithmeticPathResults(values)...), expandErr
			}
			for _, value := range values {
				path := value.path
				if path.status != StatusCompleted {
					completed = append(completed, path)
					continue
				}
				if value.expansionErr != nil {
					result := e.unresolved(path.state, fmt.Sprintf("evaluate for condition: %v", value.expansionErr), sourceLocation(loop.Cond))
					path.status = result.status
					completed = append(completed, path)
					continue
				}
				if hostUnknown || dataUnknown || value.dataUnknown {
					if status := e.reserveExecutionSteps(path.state, 1, sourceLocation(loop)); status != StatusCompleted {
						path.status = status
						completed = append(completed, path)
						continue
					}
					path.state.vars = originalVars.clone()
					exitPath := path.state.clone()
					completed = append(completed, &pathResult{state: exitPath, status: StatusCompleted})
					bodyInputs = append(bodyInputs, path)
					continue
				}
				if value.value == 0 {
					completed = append(completed, path)
				} else {
					bodyInputs = append(bodyInputs, path)
				}
			}
		}
		if err := e.checkPathGroupsMaterialization(0, sourceLocation(clause), completed, bodyInputs); err != nil {
			return append(completed, bodyInputs...), err
		}
		bodyResults, bodyErr := e.evaluateStatements(bodyInputs, clause.Do)
		if bodyErr != nil {
			return append(completed, bodyResults...), bodyErr
		}
		active = active[:0]
		for _, bodyPath := range bodyResults {
			before := len(active)
			e.collectLoopContinuation(&active, &completed, bodyPath)
			if len(active) == before {
				continue
			}
			continuation := active[len(active)-1]
			active = active[:len(active)-1]
			if loop.Post == nil {
				active = append(active, continuation)
				continue
			}
			postPaths, _, expandErr := e.evaluateArithmeticEffect(continuation.state, loop.Post, "for post expression")
			if expandErr != nil {
				return append(completed, postPaths...), expandErr
			}
			for _, path := range postPaths {
				if path.status != StatusCompleted {
					completed = append(completed, path)
					continue
				}
				active = append(active, path)
			}
		}
		if err := e.checkPathGroupsMaterialization(0, sourceLocation(clause), completed, active); err != nil {
			return append(completed, active...), err
		}
	}
	return completed, nil
}

func (e *ExecutionContext) evaluateSelect(s *State, clause *syntax.ForClause, loop *syntax.WordIter, items []string) ([]*pathResult, error) {
	s.setExitCode(0)
	active := []*pathResult{{state: s, status: StatusCompleted}}
	completed := make([]*pathResult, 0, len(items)+2)
	for len(active) > 0 {
		bodyInputs := make([]*pathResult, 0, len(active))
		next := make([]*pathResult, 0, len(active))
		for _, current := range active {
			if e.collectInactivePath(&completed, current) {
				continue
			}
			if status := e.reserveExecutionSteps(current.state, 1, sourceLocation(clause)); status != StatusCompleted {
				current.status = status
				completed = append(completed, current)
				continue
			}
			_, inputUnresolved := current.state.stdin.Data()
			if inputUnresolved {
				if status := e.reserveExecutionSteps(current.state, len(items)+1, sourceLocation(clause)); status != StatusCompleted {
					current.status = status
					completed = append(completed, current)
					continue
				}
				if err := e.checkRepeatedPathMaterialization(current, len(items)+2, sourceLocation(clause)); err != nil {
					return append(completed, current), err
				}
				eof := current.state.clone()
				eof.stdin = newCertain[[]byte](nil)
				eofStatus := e.finishSelectInput(eof, sourceLocation(clause))
				completed = append(completed, &pathResult{state: eof, status: eofStatus})
				for index, item := range items {
					choice := current.state.clone()
					choice.stdin = newCertain[[]byte](nil)
					replyAssigned, status := e.assignSelectVariable(choice, "REPLY", expand.Variable{Set: true, Kind: expand.String, Str: strconv.Itoa(index + 1)}, sourceLocation(clause))
					if status != StatusCompleted {
						completed = append(completed, &pathResult{state: choice, status: status})
						continue
					}
					if !replyAssigned {
						item = ""
					}
					_, _ = e.assignSelectVariable(choice, loop.Name.Value, expand.Variable{Set: true, Kind: expand.String, Str: item}, sourceLocation(clause))
					bodyInputs = append(bodyInputs, &pathResult{state: choice, status: StatusCompleted})
				}
				current.state.stdin = newCertain[[]byte](nil)
				// A non-empty, non-numeric representative covers Bash's invalid-input branch.
				_, status := e.assignSelectVariable(current.state, "REPLY", expand.Variable{Set: true, Kind: expand.String, Str: "?"}, sourceLocation(clause))
				if status != StatusCompleted {
					current.status = status
					completed = append(completed, current)
					continue
				}
				_, _ = e.assignSelectVariable(current.state, loop.Name.Value, expand.Variable{Set: true, Kind: expand.String}, sourceLocation(clause))
				bodyInputs = append(bodyInputs, current)
				continue
			}

			line, ok := consumeSelectInput(current.state)
			if !ok {
				current.status = e.finishSelectInput(current.state, sourceLocation(clause))
				completed = append(completed, current)
				continue
			}
			replyAssigned, status := e.assignSelectVariable(current.state, "REPLY", expand.Variable{Set: true, Kind: expand.String, Str: line}, sourceLocation(clause))
			if status != StatusCompleted {
				current.status = status
				completed = append(completed, current)
				continue
			}
			if line == "" {
				next = append(next, current)
				continue
			}
			value := ""
			if replyAssigned {
				if index, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && index > 0 && index <= len(items) {
					value = items[index-1]
				}
			}
			_, _ = e.assignSelectVariable(current.state, loop.Name.Value, expand.Variable{Set: true, Kind: expand.String, Str: value}, sourceLocation(clause))
			bodyInputs = append(bodyInputs, current)
		}
		if err := e.checkPathGroupsMaterialization(0, sourceLocation(clause), completed, next, bodyInputs); err != nil {
			paths := append(completed, next...)
			return append(paths, bodyInputs...), err
		}

		bodyResults, err := e.evaluateStatements(bodyInputs, clause.Do)
		if err != nil {
			return append(completed, bodyResults...), err
		}
		for _, bodyPath := range bodyResults {
			e.collectLoopContinuation(&next, &completed, bodyPath)
		}
		if err := e.checkPathGroupsMaterialization(0, sourceLocation(clause), completed, next); err != nil {
			return append(completed, next...), err
		}
		active = next
	}
	return completed, nil
}

func consumeSelectInput(s *State) (string, bool) {
	input, _ := s.stdin.Data()
	if len(input) == 0 {
		return "", false
	}
	if newline := bytes.IndexByte(input, '\n'); newline >= 0 {
		line := string(input[:newline])
		s.stdin = newCertain(input[newline+1:])
		return line, true
	}
	line := string(input)
	s.stdin = newCertain(input[:0])
	return line, true
}

func (e *ExecutionContext) finishSelectInput(s *State, source *location) Status {
	_, status := e.assignSelectVariable(s, "REPLY", expand.Variable{Set: true, Kind: expand.String}, source)
	s.setExitCode(1)
	return status
}

func (e *ExecutionContext) assignSelectVariable(s *State, name string, value expand.Variable, source *location) (bool, Status) {
	if err := assignShellVariable(s, name, value, false); err != nil {
		status := e.appendStreams(s, nil, []byte(fmt.Sprintf("select: %v\n", err)), false, false, source)
		return false, status
	}
	return true, StatusCompleted
}

func (e *ExecutionContext) collectLoopContinuation(active, completed *[]*pathResult, result *pathResult) {
	if result.status != StatusCompleted {
		*completed = append(*completed, result)
		return
	}
	switch result.state.signal {
	case signalBreak:
		if result.state.signalDepth > 1 {
			result.state.signalDepth--
		} else {
			result.state.signal = signalNone
			result.state.signalDepth = 0
		}
		*completed = append(*completed, result)
	case signalContinue:
		if result.state.signalDepth > 1 {
			result.state.signalDepth--
			*completed = append(*completed, result)
		} else {
			result.state.signal = signalNone
			result.state.signalDepth = 0
			*active = append(*active, result)
		}
	case signalNone:
		*active = append(*active, result)
	default:
		*completed = append(*completed, result)
	}
}
