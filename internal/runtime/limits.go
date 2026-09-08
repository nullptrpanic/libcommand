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
	if s.descriptors[1] == nil && s.descriptors[2] == nil {
		return e.appendCapturedStreams(s, stdout, stderr, stdoutUnknown, stderrUnknown, source)
	}
	if err := e.ctx.Err(); err != nil {
		e.setIssue(s, err, source)
		return StatusIncomplete
	}
	previousFS := s.fs
	s.fs = s.fs.clone()
	stdout, stderr, stdoutUnknown, stderrUnknown, err := e.applyOutputTargets(s, &redirectionPlan{descriptors: s.descriptors}, stdout, stderr, stdoutUnknown, stderrUnknown)
	if err != nil {
		s.fs = previousFS
		e.setIssue(s, err, source)
		return StatusIncomplete
	}
	previousStdout, previousStderr := s.stdout, s.stderr
	status := e.appendCapturedStreams(s, stdout, stderr, stdoutUnknown, stderrUnknown, source)
	if status == StatusCompleted {
		if err := e.checkStateMaterialization(s); err != nil {
			e.setIssue(s, err, source)
			status = StatusIncomplete
		}
	}
	if status != StatusCompleted {
		s.fs, s.stdout, s.stderr = previousFS, previousStdout, previousStderr
	}
	return status
}

// Child results have already traversed their inherited descriptor table.
// Merging their capture buffers must not redirect the bytes a second time.
func (e *ExecutionContext) appendCapturedStreams(s *State, stdout, stderr []byte, stdoutUnknown, stderrUnknown bool, source *location) Status {
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
	currentStdout = appendStream(currentStdout, stdout, &s.stdoutCursor)
	currentStderr = appendStream(currentStderr, stderr, &s.stderrCursor)
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

// All published slices are immutable prefixes. Only the first writer at the
// buffer's high-water mark may use spare capacity; a sibling or restored
// snapshot copies its prefix before writing. Unlike a shared bytes.Buffer,
// this cursor never retains a newer, larger allocation on an old snapshot.
type streamCursor struct {
	start   *byte
	written int
}

func appendStream(current, addition []byte, cursor **streamCursor) []byte {
	if len(addition) == 0 {
		return current
	}
	owned := *cursor != nil && len(current) != 0 && (*cursor).start == &current[0] && (*cursor).written == len(current)
	if !owned {
		current = append([]byte(nil), current...)
	}
	if !owned || len(addition) > cap(current)-len(current) {
		current = append(current, addition...)
		*cursor = &streamCursor{start: &current[0], written: len(current)}
		return current
	}
	current = append(current, addition...)
	(*cursor).written = len(current)
	return current
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
