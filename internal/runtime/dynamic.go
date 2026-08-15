package runtime

import (
	"fmt"
)

// evaluateSourceText evaluates dynamically parsed Shell source. Commands such
// as eval and source own their argument semantics in internal/builtin and use
// this runtime primitive through ExecutionContext.
func (e *ExecutionContext) evaluateSourceText(s *State, source, name string) ([]*pathResult, error) {
	return e.evaluateSourceTextWithParseFailure(s, source, name, 0)
}

func (e *ExecutionContext) evaluateCommandSourceText(s *State, source, name string, parseExitCode int) ([]*pathResult, error) {
	return e.evaluateSourceTextWithParseFailure(s, source, name, parseExitCode)
}

func (e *ExecutionContext) evaluateSourceTextWithParseFailure(s *State, source, name string, parseExitCode int) ([]*pathResult, error) {
	file, err := Parse(e.ctx, source, name)
	if err != nil {
		if result, resultErr, incomplete := e.incompleteFromEvaluationError(s, err, unknownLocation); incomplete {
			return []*pathResult{{state: s, status: result.status}}, resultErr
		}
		if parseExitCode != 0 {
			s.setExitCode(parseExitCode)
			status := e.appendStreams(s, nil, []byte(err.Error()+"\n"), false, false, unknownLocation)
			return []*pathResult{{state: s, status: status}}, nil
		}
		result := e.unresolved(s, fmt.Sprintf("parse %s: %v", name, err), unknownLocation)
		return []*pathResult{{state: s, status: result.status}}, nil
	}
	if len(file.Stmts) == 0 {
		s.setExitCode(0)
		return []*pathResult{{state: s, status: StatusCompleted}}, nil
	}
	e.trace.discover(file, source, name, e.trace.currentNodeID(s))
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	if err := e.candidates.addDynamic(e.ctx, file, len(source), commandCandidateLookup(e.config.LookupCommand), maximum); err != nil {
		if result, resultErr, incomplete := e.incompleteFromEvaluationError(s, err, unknownLocation); incomplete {
			return []*pathResult{{state: s, status: result.status}}, resultErr
		}
		return nil, err
	}
	if err := e.checkStateMaterialization(s); err != nil {
		return nil, err
	}
	return e.evaluateStatements([]*pathResult{{state: s, status: StatusCompleted}}, file.Stmts)
}
