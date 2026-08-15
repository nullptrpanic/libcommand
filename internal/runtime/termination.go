package runtime

import "mvdan.cc/sh/v3/syntax"

func (e *ExecutionContext) evaluateStatementsWithoutErrexit(paths []*pathResult, statements []*syntax.Stmt) ([]*pathResult, error) {
	e.errexitSuppressed++
	defer func() { e.errexitSuppressed-- }()
	return e.evaluateStatements(paths, statements)
}

func (e *ExecutionContext) evaluateStatementWithoutErrexit(s *State, statement *syntax.Stmt) ([]*pathResult, error) {
	e.errexitSuppressed++
	defer func() { e.errexitSuppressed-- }()
	return e.evaluateStatement(s, statement)
}

func (e *ExecutionContext) applyFailureEffects(paths []*pathResult, source *location) ([]*pathResult, error) {
	if e.errexitSuppressed != 0 {
		return paths, nil
	}
	result := make([]*pathResult, 0, len(paths))
	for _, path := range paths {
		if path.status != StatusCompleted || path.state.signal != signalNone {
			result = append(result, path)
			continue
		}
		exitCode, exitUnresolved := path.state.exitStatus.Data()
		if exitUnresolved {
			_, observesFailure := path.state.traps["ERR"]
			observesFailure = observesFailure && e.errTrapSuppressed == 0
			if !observesFailure && !path.state.options.errexit {
				result = append(result, path)
				continue
			}
			resolvedPaths, resolveErr := e.resolveExitStatus(path, source)
			if resolveErr != nil {
				return append(result, resolvedPaths...), resolveErr
			}
			for _, resolved := range resolvedPaths {
				if resolved.status != StatusCompleted {
					result = append(result, resolved)
					continue
				}
				resolvedExitCode, _ := resolved.state.exitStatus.Data()
				if resolvedExitCode == 0 {
					result = append(result, resolved)
					continue
				}
				failed, err := e.applyKnownFailure(resolved)
				result = append(result, failed...)
				if err != nil {
					return result, err
				}
			}
			continue
		}
		if exitCode != 0 {
			failed, err := e.applyKnownFailure(path)
			result = append(result, failed...)
			if err != nil {
				return result, err
			}
			continue
		}
		result = append(result, path)
	}
	return result, nil
}

func (e *ExecutionContext) applyKnownFailure(path *pathResult) ([]*pathResult, error) {
	originalExitCode, _ := path.state.exitStatus.Data()
	originalPipelineStatuses, originalPipelineStatusUnknown := path.state.pipelineStatusValues()
	command, trapped := path.state.traps["ERR"]
	paths := []*pathResult{path}
	if trapped && e.errTrapSuppressed == 0 {
		e.errTrapSuppressed++
		var err error
		paths, err = e.evaluateSourceText(path.state, command, "ERR trap")
		e.errTrapSuppressed--
		if err != nil {
			return paths, err
		}
	}
	for _, current := range paths {
		if current.status != StatusCompleted || current.state.signal != signalNone {
			continue
		}
		current.state.setExitCode(originalExitCode)
		current.state.setPipelineStatusValues(originalPipelineStatuses, originalPipelineStatusUnknown)
		if current.state.options.errexit {
			current.state.signal = signalExit
		}
	}
	return paths, nil
}

func (e *ExecutionContext) evaluateExitTraps(paths []*pathResult) ([]*pathResult, error) {
	results := make([]*pathResult, 0, len(paths))
	for _, path := range paths {
		command, exists := path.state.traps["EXIT"]
		if !exists || path.state.exitTrapInherited || path.status != StatusCompleted {
			results = append(results, path)
			continue
		}
		originalExitStatus := path.state.exitStatus
		originalExitFailure := path.state.exitFailure
		path.state.deleteTrap("EXIT")
		path.state.signal = signalNone
		trapped, err := e.evaluateSourceText(path.state, command, "EXIT trap")
		for _, current := range trapped {
			if current.status != StatusCompleted || current.state.signal != signalNone {
				continue
			}
			current.state.exitStatus = originalExitStatus
			current.state.exitFailure = originalExitFailure
		}
		results = append(results, trapped...)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func clearInheritedExitTrap(s *State) {
	if _, exists := s.traps["EXIT"]; exists {
		s.exitTrapInherited = true
	}
}
