package runtime

import (
	"fmt"

	"github.com/nullptrpanic/libcommand/internal/materialize"
)

const defaultMaxMemoryBytes = materialize.DefaultMaxBytes

func normalizedMaxMemoryBytes(maximum int) int {
	if maximum == 0 {
		return defaultMaxMemoryBytes
	}
	return maximum
}

func (e *ExecutionContext) reserveExecutionSteps(s *State, count int, source *location) Status {
	if status := e.checkContext(s, source); status != StatusCompleted {
		return status
	}
	if count <= 0 || count > e.config.MaxExecutionSteps-e.executedSteps {
		e.setIssue(s, fmt.Errorf("maximum execution step count %d reached", e.config.MaxExecutionSteps), source)
		return StatusIncomplete
	}
	e.executedSteps += count
	return StatusCompleted
}

func (e *ExecutionContext) releaseExecutionStepOnSubstitution(err error) bool {
	if !expansionRequested(err) {
		return false
	}
	e.executedSteps--
	return true
}

func (e *ExecutionContext) appendStreams(s *State, stdout, stderr []byte, stdoutUnknown, stderrUnknown bool, source *location) Status {
	if err := e.ctx.Err(); err != nil {
		e.setIssue(s, err, source)
		return StatusIncomplete
	}
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	currentStdout, currentStdoutUnresolved := s.stdout.Data()
	currentStderr, currentStderrUnresolved := s.stderr.Data()
	total := 0
	ok := true
	for _, size := range []int{len(currentStdout), len(currentStderr), len(stdout), len(stderr)} {
		total, ok = materialize.Add(total, size, maximum)
		if !ok {
			e.setIssue(s, materialize.LimitError(maximum), source)
			return StatusIncomplete
		}
	}
	currentStdout = append(append([]byte(nil), currentStdout...), stdout...)
	currentStderr = append(append([]byte(nil), currentStderr...), stderr...)
	if currentStdoutUnresolved || stdoutUnknown {
		s.stdout = newUnresolved(currentStdout)
	} else {
		s.stdout = newCertain(currentStdout)
	}
	if currentStderrUnresolved || stderrUnknown {
		s.stderr = newUnresolved(currentStderr)
	} else {
		s.stderr = newCertain(currentStderr)
	}
	return StatusCompleted
}

func (e *ExecutionContext) appendOutcome(s *State, result *outcome, source *location) Status {
	if len(result.stdout) == 0 && len(result.stderr) == 0 && !result.stdoutUnknown && !result.stderrUnknown {
		return result.status
	}
	if status := e.appendStreams(s, result.stdout, result.stderr, result.stdoutUnknown, result.stderrUnknown, source); status != StatusCompleted {
		return status
	}
	return result.status
}

func (e *ExecutionContext) checkContext(s *State, source *location) Status {
	if err := e.ctx.Err(); err != nil {
		e.setIssue(s, err, source)
		return StatusIncomplete
	}
	return StatusCompleted
}
