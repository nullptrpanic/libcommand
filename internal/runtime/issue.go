package runtime

import (
	"context"
	"errors"
	"fmt"
)

func (e *ExecutionContext) incompleteEvaluationIssue(err error) (error, bool) {
	if contextErr := e.ctx.Err(); contextErr != nil {
		return contextErr, true
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return nil, false
	}
	return err, true
}

func (e *ExecutionContext) incompleteFromEvaluationError(s *State, err error, source *location) (*outcome, error, bool) {
	issue, incomplete := e.incompleteEvaluationIssue(err)
	if !incomplete {
		return nil, nil, false
	}
	e.setIssue(s, issue, source)
	if errors.Is(issue, context.Canceled) || errors.Is(issue, context.DeadlineExceeded) {
		return &outcome{status: StatusIncomplete}, issue, true
	}
	return &outcome{status: StatusIncomplete}, nil, true
}

func (e *ExecutionContext) outcomeFromExpansionError(s *State, err error, message string, source *location) (*outcome, error) {
	if e.releaseExecutionStepOnSubstitution(err) {
		return &outcome{}, err
	}
	if result, resultErr, incomplete := e.incompleteFromEvaluationError(s, err, source); incomplete {
		return result, resultErr
	}
	return e.unresolved(s, message, source), nil
}

func (e *ExecutionContext) unresolvedPath(s *State, message string, source *location) []*pathResult {
	result := e.unresolved(s, message, source)
	return []*pathResult{{state: s, status: result.status}}
}

func (e *ExecutionContext) pathsFromEvaluationError(s *State, err error, source *location) ([]*pathResult, error) {
	if e.releaseExecutionStepOnSubstitution(err) {
		return nil, err
	}
	if result, resultErr, incomplete := e.incompleteFromEvaluationError(s, err, source); incomplete {
		return []*pathResult{{state: s, status: result.status}}, resultErr
	}
	return e.unresolvedPath(s, err.Error(), source), nil
}

func (e *ExecutionContext) unresolved(s *State, message string, source *location) *outcome {
	e.setIssue(s, errors.New(message), source)
	return &outcome{status: StatusUnresolved}
}

func (e *ExecutionContext) setIssue(s *State, issue error, source *location) {
	if s.issue != nil {
		return
	}
	if source == nil || source.line == 0 {
		s.issue = issue
		return
	}
	s.issue = fmt.Errorf("%w at %d:%d", issue, source.line, source.column)
}
