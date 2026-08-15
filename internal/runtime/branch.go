package runtime

import (
	"errors"
	"fmt"
	"regexp"

	"mvdan.cc/sh/v3/pattern"
	"mvdan.cc/sh/v3/syntax"
)

func (e *ExecutionContext) evaluateIf(s *State, clause *syntax.IfClause) ([]*pathResult, error) {
	if len(clause.Cond) == 0 {
		return e.evaluateStatements([]*pathResult{{state: s, status: StatusCompleted}}, clause.Then)
	}
	conditionPaths, err := e.evaluateStatementsWithoutErrexit([]*pathResult{{state: s, status: StatusCompleted}}, clause.Cond)
	if err != nil {
		return conditionPaths, err
	}
	results := make([]*pathResult, 0, len(conditionPaths))
	for _, conditionPath := range conditionPaths {
		if conditionPath.status != StatusCompleted {
			results = append(results, conditionPath)
			continue
		}
		resolvedPaths, resolveErr := e.resolveExitStatus(conditionPath, sourceLocation(clause))
		if resolveErr != nil {
			return append(results, resolvedPaths...), resolveErr
		}
		for _, resolved := range resolvedPaths {
			if e.stop {
				resolved.status = StatusTerminated
				results = append(results, resolved)
				continue
			}
			exitCode, _ := resolved.state.exitStatus.Data()
			matched := exitCode == 0
			var branchPaths []*pathResult
			if matched {
				branchPaths, err = e.evaluateStatements([]*pathResult{resolved}, clause.Then)
			} else if clause.Else != nil {
				branchPaths, err = e.evaluateIf(resolved.state, clause.Else)
			} else {
				resolved.state.setExitCode(0)
				branchPaths = []*pathResult{resolved}
			}
			results = append(results, branchPaths...)
			if err != nil {
				return results, err
			}
		}
	}
	return results, nil
}

func (e *ExecutionContext) evaluateLogical(s *State, command *syntax.BinaryCmd) ([]*pathResult, error) {
	leftPaths, err := e.evaluateStatementWithoutErrexit(s, command.X)
	if err != nil {
		return leftPaths, err
	}
	results := make([]*pathResult, 0, len(leftPaths))
	for _, leftPath := range leftPaths {
		if leftPath.status != StatusCompleted {
			results = append(results, leftPath)
			continue
		}
		resolvedPaths, resolveErr := e.resolveExitStatus(leftPath, sourceLocation(command))
		if resolveErr != nil {
			return append(results, resolvedPaths...), resolveErr
		}
		for _, resolved := range resolvedPaths {
			if e.stop {
				resolved.status = StatusTerminated
				results = append(results, resolved)
				continue
			}
			exitCode, _ := resolved.state.exitStatus.Data()
			succeeded := exitCode == 0
			runRight := command.Op == syntax.AndStmt && succeeded || command.Op == syntax.OrStmt && !succeeded
			if !runRight {
				results = append(results, resolved)
				continue
			}
			rightPaths, err := e.evaluateStatement(resolved.state, command.Y)
			results = append(results, rightPaths...)
			if err != nil {
				return results, err
			}
		}
	}
	return results, nil
}

func (e *ExecutionContext) evaluateTest(s *State, clause *syntax.TestClause) ([]*pathResult, error) {
	if status := e.reserveExecutionSteps(s, 1, sourceLocation(clause)); status != StatusCompleted {
		return []*pathResult{{state: s, status: status}}, nil
	}
	value, err := e.testTruth(s, clause.X)
	if err != nil {
		if e.releaseExecutionStepOnSubstitution(err) {
			return nil, err
		}
		if result, resultErr, incomplete := e.incompleteFromEvaluationError(s, err, sourceLocation(clause)); incomplete {
			return []*pathResult{{state: s, status: result.status}}, resultErr
		}
		var failure *testFailure
		if errors.As(err, &failure) {
			s.setExitCode(failure.exitCode)
			status := e.appendStreams(s, nil, []byte("[[: "+failure.message+"\n"), false, false, sourceLocation(clause))
			return []*pathResult{{state: s, status: status}}, nil
		}
		if isUnknownValueError(err) {
			return e.unresolvedPath(s, err.Error(), sourceLocation(clause)), nil
		}
		value = truthUnknown
	}
	if value != truthUnknown {
		matched := value == truthTrue
		s.setExitCode(boolExitCode(matched))
		return []*pathResult{{state: s, status: StatusCompleted}}, nil
	}

	s.setUnknownExitCode()
	return e.resolveExitStatus(&pathResult{state: s, status: StatusCompleted}, sourceLocation(clause))
}

func (e *ExecutionContext) evaluateCase(s *State, clause *syntax.CaseClause) ([]*pathResult, error) {
	subjectUnknown := wordCertainty(s, clause.Word) != 0
	subject := ""
	var err error
	subject, err = e.literalValue(s, clause.Word)
	if err != nil {
		if expansionRequested(err) {
			return nil, err
		}
		if result, resultErr, incomplete := e.incompleteFromEvaluationError(s, err, sourceLocation(clause)); incomplete {
			return []*pathResult{{state: s, status: result.status}}, resultErr
		}
		if isUnknownValueError(err) {
			return e.unresolvedPath(s, err.Error(), sourceLocation(clause)), nil
		}
		if !subjectUnknown {
			return e.unresolvedPath(s, fmt.Sprintf("expand case word: %v", err), sourceLocation(clause)), nil
		}
	}
	type casePath struct {
		path     *pathResult
		forced   bool
		executed bool
	}
	pathResults := func(paths []*casePath) []*pathResult {
		results := make([]*pathResult, 0, len(paths))
		for _, path := range paths {
			results = append(results, path.path)
		}
		return results
	}
	active := []*casePath{{path: &pathResult{state: s, status: StatusCompleted}}}
	finished := make([]*pathResult, 0, len(clause.Items)+1)
	for _, item := range clause.Items {
		next := make([]*casePath, 0, len(active)+1)
		for _, current := range active {
			if current.path.status != StatusCompleted || e.stop {
				if e.stop && current.path.status == StatusCompleted {
					current.path.status = StatusTerminated
				}
				finished = append(finished, current.path)
				continue
			}
			evaluations := []*caseTruthResult{{path: current.path, value: truthTrue}}
			if !current.forced {
				var matchErr error
				evaluations, matchErr = e.evaluateCaseItemTruth(current.path.state, subject, subjectUnknown, item)
				if matchErr != nil {
					paths := make([]*pathResult, 0, len(evaluations))
					for _, evaluation := range evaluations {
						paths = append(paths, evaluation.path)
					}
					return append(finished, paths...), matchErr
				}
			}
			for _, evaluation := range evaluations {
				path := evaluation.path
				match := evaluation.value
				if path.status != StatusCompleted {
					finished = append(finished, path)
					continue
				}
				if match == truthUnknown {
					if status := e.reserveExecutionSteps(path.state, 1, sourceLocation(item)); status != StatusCompleted {
						path.status = status
						finished = append(finished, path)
						continue
					}
				}
				if match != truthTrue {
					next = append(next, &casePath{path: path, executed: current.executed})
				}
				if match == truthFalse {
					continue
				}
				branchValue := *path
				branch := &branchValue
				if match == truthUnknown {
					if err := e.checkRepeatedPathMaterialization(path, 2, sourceLocation(item)); err != nil {
						return append(finished, path), err
					}
					branch.state = path.state.clone()
				}
				activeBranches := pathResults(next)
				if err := e.checkPathGroupsMaterialization(0, sourceLocation(item), finished, activeBranches, []*pathResult{branch}); err != nil {
					paths := append(finished, activeBranches...)
					return append(paths, branch), err
				}
				branchPaths, err := e.evaluateStatements([]*pathResult{branch}, item.Stmts)
				if err != nil {
					return append(finished, branchPaths...), err
				}
				for _, branchPath := range branchPaths {
					if branchPath.status != StatusCompleted || e.stop || item.Op == syntax.Break {
						if e.stop && branchPath.status == StatusCompleted {
							branchPath.status = StatusTerminated
						}
						finished = append(finished, branchPath)
						continue
					}
					next = append(next, &casePath{
						path:     branchPath,
						forced:   item.Op == syntax.Fallthrough,
						executed: true,
					})
				}
			}
		}
		activePaths := pathResults(next)
		if err := e.checkPathGroupsMaterialization(0, sourceLocation(item), finished, activePaths); err != nil {
			return append(finished, activePaths...), err
		}
		active = next
	}
	for _, current := range active {
		if !current.executed {
			current.path.state.setExitCode(0)
		}
		finished = append(finished, current.path)
	}
	return finished, nil
}

func (e *ExecutionContext) collectInactivePath(completed *[]*pathResult, current *pathResult) bool {
	if current.status == StatusCompleted && !e.stop {
		return false
	}
	if e.stop && current.status == StatusCompleted {
		current.status = StatusTerminated
	}
	*completed = append(*completed, current)
	return true
}

type caseTruthResult struct {
	path  *pathResult
	value truthValue
}

func (e *ExecutionContext) evaluateCaseItemTruth(s *State, subject string, subjectUnknown bool, item *syntax.CaseItem) ([]*caseTruthResult, error) {
	s.pushSubstitutionFrame()
	results, err := e.resumeCaseItemTruth(s, subject, subjectUnknown, item)
	if len(results) == 0 {
		s.popSubstitutionFrame()
	}
	for _, result := range results {
		result.path.state.popSubstitutionFrame()
	}
	return results, err
}

func (e *ExecutionContext) resumeCaseItemTruth(s *State, subject string, subjectUnknown bool, item *syntax.CaseItem) ([]*caseTruthResult, error) {
	value, err := e.caseItemTruth(s, subject, subjectUnknown, item)
	request, requested := requestedSubstitution(err)
	if !requested {
		status := StatusCompleted
		if result, resultErr, incomplete := e.incompleteFromEvaluationError(s, err, sourceLocation(item)); incomplete {
			status = result.status
			err = resultErr
		} else if isUnknownValueError(err) {
			result := e.unresolved(s, err.Error(), sourceLocation(item))
			status = result.status
			err = nil
		}
		return []*caseTruthResult{{path: &pathResult{state: s, status: status}, value: value}}, err
	}
	substitutionPaths, substitutionErr := e.evaluateSubstitutionPaths(request.state, request.substitution)
	results := make([]*caseTruthResult, 0, len(substitutionPaths))
	for _, substitutionPath := range substitutionPaths {
		if substitutionPath.status != StatusCompleted {
			results = append(results, &caseTruthResult{path: substitutionPath, value: truthUnknown})
			continue
		}
		resumed, resumeErr := e.resumeCaseItemTruth(substitutionPath.state, subject, subjectUnknown, item)
		results = append(results, resumed...)
		if resumeErr != nil {
			return results, resumeErr
		}
	}
	return results, substitutionErr
}

func (e *ExecutionContext) caseItemTruth(s *State, subject string, subjectUnknown bool, item *syntax.CaseItem) (truthValue, error) {
	unknown := false
	for _, patternWord := range item.Patterns {
		candidate, err := e.casePattern(s, patternWord)
		if err != nil {
			if expansionRequested(err) {
				return truthUnknown, err
			}
			if _, incomplete := e.incompleteEvaluationIssue(err); incomplete {
				return truthUnknown, err
			}
			if isUnknownValueError(err) {
				return truthUnknown, err
			}
			unknown = true
			continue
		}
		if candidate == "*" {
			return truthTrue, nil
		}
		if subjectUnknown {
			unknown = true
			continue
		}
		regularExpression, err := pattern.Regexp(candidate, pattern.EntireString)
		if err != nil {
			unknown = true
			continue
		}
		if regexp.MustCompile(regularExpression).MatchString(subject) {
			return truthTrue, nil
		}
	}
	if unknown {
		return truthUnknown, nil
	}
	return truthFalse, nil
}

func (e *ExecutionContext) casePattern(s *State, word *syntax.Word) (string, error) {
	certainty := wordCertainty(s, word)
	value, err := e.patternValue(s, word)
	if err != nil {
		return "", err
	}
	if certainty.hostUnknown() {
		return "", fmt.Errorf("case pattern depends on host runtime state")
	}
	if certainty.dataUnknown() {
		return "", fmt.Errorf("case pattern depends on unresolved command output")
	}
	return value, nil
}
