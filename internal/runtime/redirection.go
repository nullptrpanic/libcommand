package runtime

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"maps"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type redirectionFailure struct {
	message string
}

func (failure *redirectionFailure) Error() string {
	return failure.message
}

func (e *ExecutionContext) applyOutputTargets(s *State, plan *redirectionPlan, stdout, stderr []byte, stdoutUnknown, stderrUnknown bool) ([]byte, []byte, bool, bool, error) {
	var capturedStdout []byte
	var capturedStderr []byte
	var capturedStdoutUnknown bool
	var capturedStderrUnknown bool
	write := func(target *outputTarget, contents []byte, unknown bool) error {
		if target == nil || target.external {
			return nil
		}
		if target.file != "" {
			// Opening a non-append target already truncated it. Subsequent
			// command writes must be visible immediately and retain prior writes.
			return s.fs.writeValue(target.file, contents, true, unknown)
		}
		if target.captureFD == 1 {
			capturedStdout = append(capturedStdout, contents...)
			capturedStdoutUnknown = capturedStdoutUnknown || unknown
			return nil
		}
		capturedStderr = append(capturedStderr, contents...)
		capturedStderrUnknown = capturedStderrUnknown || unknown
		return nil
	}
	stdoutTarget := descriptorOutput(plan.descriptors, 1)
	stderrTarget := descriptorOutput(plan.descriptors, 2)
	if stdoutTarget != nil && stderrTarget != nil && stdoutTarget.id == stderrTarget.id {
		combined := append(append([]byte(nil), stdout...), stderr...)
		if err := write(stdoutTarget, combined, stdoutUnknown || stderrUnknown); err != nil {
			return nil, nil, false, false, err
		}
	} else {
		if err := write(stdoutTarget, stdout, stdoutUnknown); err != nil {
			return nil, nil, false, false, err
		}
		if err := write(stderrTarget, stderr, stderrUnknown); err != nil {
			return nil, nil, false, false, err
		}
	}
	return capturedStdout, capturedStderr, capturedStdoutUnknown, capturedStderrUnknown, nil
}

func descriptorOutput(descriptors map[int]*descriptorTarget, fd int) *outputTarget {
	if descriptor := descriptors[fd]; descriptor != nil {
		return descriptor.output
	}
	return &outputTarget{id: fd, captureFD: fd}
}

func (e *ExecutionContext) inputOnlySubstitution(s *State, substitution *syntax.CmdSubst) (*substitutionResult, bool, error) {
	if len(substitution.Stmts) != 1 {
		return nil, false, nil
	}
	statement := substitution.Stmts[0]
	if len(statement.Redirs) != 1 || statement.Redirs[0].Op != syntax.RdrIn {
		return nil, false, nil
	}
	if statement.Cmd != nil {
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) != 0 || len(call.Assigns) != 0 {
			return nil, false, nil
		}
	}
	filename, unresolved, err := e.redirectWord(s, statement.Redirs[0].Word)
	if err != nil {
		return nil, true, err
	}
	if unresolved || isExternalDevicePath(filename) {
		return &substitutionResult{
			stdout:     newUnresolved[[]byte](nil),
			exitStatus: newUnresolved(0),
		}, true, nil
	}
	directory, _ := s.dir.Data()
	filename = s.fs.resolve(directory, filename)
	contents, unknown, exists := s.fs.readFile(filename)
	if !exists {
		if err := s.fs.writeWithParents(filename, nil, false); err != nil {
			return nil, true, outputRedirectionError(filename, err)
		}
		return &substitutionResult{stdout: newCertain[[]byte](nil), exitStatus: newCertain(0)}, true, nil
	}
	stdout := newCertain(contents)
	if unknown {
		stdout = newUnresolved(contents)
	}
	return &substitutionResult{stdout: stdout, exitStatus: newCertain(0)}, true, nil
}

func (e *ExecutionContext) prepareRedirections(s *State, redirections []*syntax.Redirect) (*redirectionPlan, error) {
	base := maps.Clone(s.descriptors)
	if base == nil {
		base = make(map[int]*descriptorTarget)
	}
	if base[0] == nil {
		base[0] = &descriptorTarget{key: new(byte), input: s.stdin}
	}
	if base[1] == nil {
		base[1] = &descriptorTarget{key: new(byte), output: &outputTarget{id: 1, captureFD: 1}}
	}
	if base[2] == nil {
		base[2] = &descriptorTarget{key: new(byte), output: &outputTarget{id: 2, captureFD: 2}}
	}
	input := *base[0]
	input.input = s.stdin
	base[0] = &input
	plan := &redirectionPlan{descriptors: maps.Clone(base), saved: make(map[int]*descriptorTarget), bindings: make(map[int]*byte)}
	nextTargetID := 3
	for _, descriptor := range base {
		if descriptor.output != nil {
			nextTargetID = max(nextTargetID, descriptor.output.id+1)
		}
	}
	directory, _ := s.dir.Data()
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	openedBytes, metadataIndex := 0, 0
	checkPlan := func() error {
		for _, redirect := range plan.redirects[metadataIndex:] {
			var ok bool
			openedBytes, ok = materialize.Add(openedBytes, materialize.EntryBytes+len(redirect.Target), maximum)
			if !ok {
				return materialize.LimitError(maximum)
			}
		}
		metadataIndex = len(plan.redirects)
		if _, ok := materialize.Add(openedBytes, len(plan.descriptors)*materialize.EntryBytes, maximum); !ok {
			return materialize.LimitError(maximum)
		}
		return nil
	}
	chargeInput := func(size int) error {
		var ok bool
		openedBytes, ok = materialize.Add(openedBytes, size, maximum)
		if !ok {
			return materialize.LimitError(maximum)
		}
		return nil
	}
	for _, redirection := range redirections {
		if err := e.ctx.Err(); err != nil {
			return nil, err
		}
		if err := checkPlan(); err != nil {
			return nil, err
		}
		fd, err := redirectFD(redirection)
		if err != nil {
			return nil, err
		}
		switch redirection.Op {
		case syntax.RdrIn, syntax.RdrInOut:
			filename, unresolved, err := e.redirectWord(s, redirection.Word)
			if err != nil {
				return nil, err
			}
			if unresolved {
				target := &descriptorTarget{input: newUnresolved[[]byte](nil)}
				if redirection.Op == syntax.RdrInOut {
					target.output = &outputTarget{id: nextTargetID, external: true}
					nextTargetID++
				}
				plan.descriptors[fd] = target
				plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Unresolved: true})
				if fd == 0 {
					plan.stdinReplaced = true
				}
				continue
			}
			filename = s.fs.resolve(directory, filename)
			if isExternalDevicePath(filename) {
				target := &descriptorTarget{input: newUnresolved[[]byte](nil), inputFile: filename}
				if redirection.Op == syntax.RdrInOut {
					target.output = &outputTarget{id: nextTargetID, external: true, file: filename}
					nextTargetID++
				}
				plan.descriptors[fd] = target
				plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Target: filename})
				if fd == 0 {
					plan.stdinReplaced = true
				}
				continue
			}
			contents, unknown, exists := s.fs.readFile(filename)
			if !exists {
				if err := s.fs.writeWithParents(filename, nil, false); err != nil {
					return nil, outputRedirectionError(filename, err)
				}
				contents = nil
				unknown = false
			}
			target := &descriptorTarget{inputFile: filename}
			if err := chargeInput(len(contents)); err != nil {
				return nil, err
			}
			if unknown {
				target.input = newUnresolved(contents)
			} else {
				target.input = newCertain(contents)
			}
			if redirection.Op == syntax.RdrInOut {
				target.output = &outputTarget{id: nextTargetID, file: filename}
				nextTargetID++
			}
			plan.descriptors[fd] = target
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Target: filename})
			if fd == 0 {
				plan.stdinReplaced = true
			}
		case syntax.WordHdoc:
			value, unknown, err := e.redirectLiteralValue(s, redirection.Word)
			if err != nil {
				return nil, fmt.Errorf("expand here-string redirection: %w", err)
			}
			input := newCertain([]byte(value + "\n"))
			if err := chargeInput(len(value) + 1); err != nil {
				return nil, err
			}
			if unknown {
				input = newUnresolved([]byte(value + "\n"))
			}
			plan.descriptors[fd] = &descriptorTarget{input: input}
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Unresolved: unknown})
			if fd == 0 {
				plan.stdinReplaced = true
			}
		case syntax.Hdoc, syntax.DashHdoc:
			value, unknown, err := e.redirectLiteralValue(s, redirection.Hdoc)
			if err != nil {
				return nil, fmt.Errorf("expand here-document: %w", err)
			}
			input := newCertain([]byte(value))
			if err := chargeInput(len(value)); err != nil {
				return nil, err
			}
			if unknown {
				input = newUnresolved([]byte(value))
			}
			plan.descriptors[fd] = &descriptorTarget{input: input}
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Unresolved: unknown})
			if fd == 0 {
				plan.stdinReplaced = true
			}
		case syntax.RdrOut, syntax.RdrClob, syntax.AppOut:
			filename, unresolved, err := e.redirectWord(s, redirection.Word)
			if err != nil {
				return nil, err
			}
			appendMode := redirection.Op == syntax.AppOut
			output, err := plan.openOutput(s, directory, filename, unresolved, appendMode, nextTargetID)
			if err != nil {
				return nil, err
			}
			nextTargetID++
			plan.descriptors[fd] = &descriptorTarget{output: output}
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Target: output.file, Unresolved: unresolved})
			if fd == 0 {
				plan.stdinReplaced = true
			}
		case syntax.DplOut:
			target, unresolved, err := e.redirectWord(s, redirection.Word)
			if err != nil {
				return nil, err
			}
			if unresolved {
				plan.descriptors[fd] = abstractDescriptor(nextTargetID)
				nextTargetID++
				plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Unresolved: true})
				if fd == 0 {
					plan.stdinReplaced = true
				}
				continue
			}
			if targetFD, parseErr := strconv.Atoi(target); parseErr == nil {
				descriptor := plan.descriptors[targetFD]
				if descriptor == nil {
					descriptor = abstractDescriptor(nextTargetID)
					nextTargetID++
				}
				plan.descriptors[fd] = descriptor
			} else if target == "-" {
				plan.descriptors[fd] = &descriptorTarget{}
			} else if redirection.N == nil && fd == 1 {
				output, err := plan.openOutput(s, directory, target, false, false, nextTargetID)
				if err != nil {
					return nil, err
				}
				nextTargetID++
				descriptor := &descriptorTarget{output: output}
				plan.descriptors[1] = descriptor
				plan.descriptors[2] = descriptor
				target = output.file
			} else {
				return nil, &redirectionFailure{message: fmt.Sprintf("%s: ambiguous redirect", target)}
			}
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Target: target})
			if fd == 0 {
				plan.stdinReplaced = true
			}
		case syntax.DplIn:
			target, unresolved, err := e.redirectWord(s, redirection.Word)
			if err != nil {
				return nil, err
			}
			if unresolved {
				plan.descriptors[fd] = abstractDescriptor(nextTargetID)
				nextTargetID++
			} else if target == "-" {
				plan.descriptors[fd] = &descriptorTarget{}
			} else if targetFD, parseErr := strconv.Atoi(target); parseErr == nil {
				descriptor := plan.descriptors[targetFD]
				if descriptor == nil {
					descriptor = abstractDescriptor(nextTargetID)
					nextTargetID++
				}
				plan.descriptors[fd] = descriptor
			} else {
				return nil, &redirectionFailure{message: fmt.Sprintf("%s: ambiguous redirect", target)}
			}
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Target: target, Unresolved: unresolved})
			if fd == 0 {
				plan.stdinReplaced = true
			}
		case syntax.RdrAll, syntax.AppAll:
			filename, unresolved, err := e.redirectWord(s, redirection.Word)
			if err != nil {
				return nil, err
			}
			appendMode := redirection.Op == syntax.AppAll
			output, err := plan.openOutput(s, directory, filename, unresolved, appendMode, nextTargetID)
			if err != nil {
				return nil, err
			}
			nextTargetID++
			descriptor := &descriptorTarget{output: output}
			plan.descriptors[1] = descriptor
			plan.descriptors[2] = descriptor
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Target: output.file, Unresolved: unresolved})
		default:
			return nil, fmt.Errorf("unsupported redirection operator %v", redirection.Op)
		}
	}
	if err := checkPlan(); err != nil {
		return nil, err
	}
	for fd, descriptor := range plan.descriptors {
		if descriptor == base[fd] {
			continue
		}
		if descriptor.key == nil {
			descriptor.key = new(byte)
		}
		plan.saved[fd] = base[fd]
		plan.bindings[fd] = descriptor.key
	}
	return plan, nil
}

func (plan *redirectionPlan) openOutput(s *State, directory, filename string, unresolved, appendMode bool, id int) (*outputTarget, error) {
	if unresolved {
		return &outputTarget{id: id, external: true}, nil
	}
	filename = s.fs.resolve(directory, filename)
	if isExternalDevicePath(filename) {
		return &outputTarget{id: id, external: true, file: filename}, nil
	}
	if err := prepareOutputRedirection(s, filename, appendMode); err != nil {
		return nil, err
	}
	if !appendMode {
		plan.truncateInputs(filename)
	}
	return &outputTarget{id: id, file: filename, append: appendMode}, nil
}

// Descriptor tables and targets are immutable between mutations. Forks share
// them, while descriptor aliases within one path advance together.
func (s *State) setInput(input *uncertain[[]byte]) {
	previous := s.stdin
	s.stdin = input
	s.updateDescriptorInput(previous, input)
}

func (s *State) updateDescriptorInput(previous, next *uncertain[[]byte]) {
	var updated map[int]*descriptorTarget
	for fd, descriptor := range s.descriptors {
		if descriptor.input != previous {
			continue
		}
		if updated == nil {
			updated = maps.Clone(s.descriptors)
		}
		copy := *descriptor
		copy.input = next
		updated[fd] = &copy
	}
	if updated != nil {
		s.setDescriptors(updated)
	}
}

func (s *State) setDescriptors(descriptors map[int]*descriptorTarget) {
	s.descriptors = descriptors
	s.descriptorBytes = descriptorMaterialization(descriptors, s.stdin)
}

func descriptorMaterialization(descriptors map[int]*descriptorTarget, stdin *uncertain[[]byte]) int {
	total := 0
	inputs := map[*uncertain[[]byte]]bool{stdin: true}
	for _, descriptor := range descriptors {
		size := materialize.EntryBytes
		if descriptor != nil {
			size += len(descriptor.inputFile)
			if descriptor.output != nil {
				size += len(descriptor.output.file)
			}
			if descriptor.input != nil && !inputs[descriptor.input] {
				inputs[descriptor.input] = true
				value, _ := descriptor.input.Data()
				size += len(value)
			}
		}
		var ok bool
		total, ok = materialize.Add(total, size, math.MaxInt)
		if !ok {
			return math.MaxInt
		}
	}
	return total
}

func (s *State) bindInput(input *uncertain[[]byte]) {
	s.stdin = input
	descriptors := maps.Clone(s.descriptors)
	if descriptors == nil {
		descriptors = make(map[int]*descriptorTarget)
	}
	descriptors[0] = &descriptorTarget{key: new(byte), input: input}
	s.setDescriptors(descriptors)
}

func (s *State) restoreRedirections(plan *redirectionPlan) {
	s.redirectionFrames = s.redirectionFrames[:len(s.redirectionFrames)-1]
	s.redirectionBytes -= plan.retainedBytes
	if s.keptRedirections == plan {
		s.keptRedirections = nil
		return
	}
	updated := maps.Clone(s.descriptors)
	for fd, saved := range plan.saved {
		if saved == nil {
			delete(updated, fd)
			continue
		}
		// A descriptor duplicated into stdin may have been consumed. Restore
		// the previous binding, not its stale input cursor.
		for _, descriptor := range s.descriptors {
			if descriptor.key == saved.key {
				saved = descriptor
				break
			}
		}
		updated[fd] = saved
	}
	if _, replaced := plan.saved[0]; replaced {
		s.stdin = updated[0].input
		if s.stdin == nil {
			s.stdin = newCertain[[]byte](nil)
		}
	}
	s.setDescriptors(updated)
}

// captureDescriptor installs a pipeline/substitution capture without changing
// other inherited file descriptors.
func (s *State) captureDescriptor(fd int) {
	if s.descriptors == nil {
		return
	}
	descriptors := maps.Clone(s.descriptors)
	descriptors[fd] = &descriptorTarget{key: new(byte), output: &outputTarget{id: fd, captureFD: fd}}
	s.setDescriptors(descriptors)
}

func (s *State) commandRedirects(active []*Redirect) []*Redirect {
	baseline := maps.Clone(s.descriptors)
	for index := len(s.redirectionFrames) - 1; index >= 0; index-- {
		for fd, saved := range s.redirectionFrames[index].saved {
			if saved == nil {
				delete(baseline, fd)
			} else {
				baseline[fd] = saved
			}
		}
	}
	var descriptors []int
	for fd := range baseline {
		descriptors = append(descriptors, fd)
	}
	sort.Ints(descriptors)
	var redirects []*Redirect
	for _, fd := range descriptors {
		descriptor := baseline[fd]
		if descriptor.inputFile != "" && descriptor.output != nil && descriptor.output.file == descriptor.inputFile {
			redirects = append(redirects, &Redirect{FD: fd, Operator: "<>", Target: descriptor.inputFile})
			continue
		}
		if descriptor.inputFile != "" {
			redirects = append(redirects, &Redirect{FD: fd, Operator: "<", Target: descriptor.inputFile})
		}
		if output := descriptor.output; output != nil && output.file != "" {
			op := ">"
			if output.append {
				op = ">>"
			}
			redirects = append(redirects, &Redirect{FD: fd, Operator: op, Target: output.file})
		}
	}
	redirects = append(redirects, cloneRedirects(active)...)
	bindings := make(map[int]*byte)
	for _, frame := range s.redirectionFrames {
		for fd, key := range frame.bindings {
			bindings[fd] = key
		}
	}
	var replaced []int
	for fd, key := range bindings {
		if descriptor := s.descriptors[fd]; descriptor != nil && descriptor.key != key {
			replaced = append(replaced, fd)
		}
	}
	sort.Ints(replaced)
	for _, fd := range replaced {
		descriptor := s.descriptors[fd]
		redirect := &Redirect{FD: fd, Operator: "<&", Unresolved: true}
		if descriptor.inputFile != "" {
			redirect.Operator, redirect.Target, redirect.Unresolved = "<", descriptor.inputFile, false
		}
		if descriptor.output != nil && descriptor.output.file != "" {
			redirect.Operator, redirect.Target, redirect.Unresolved = ">", descriptor.output.file, false
			if descriptor.inputFile == descriptor.output.file {
				redirect.Operator = "<>"
			}
		}
		redirects = append(redirects, redirect)
	}
	return redirects
}

func abstractDescriptor(targetID int) *descriptorTarget {
	return &descriptorTarget{
		input:  newUnresolved[[]byte](nil),
		output: &outputTarget{id: targetID, external: true},
	}
}

func (plan *redirectionPlan) truncateInputs(filename string) {
	for fd, descriptor := range plan.descriptors {
		if descriptor.inputFile != filename {
			continue
		}
		copy := *descriptor
		truncateDescriptorInput(&copy, filename)
		plan.descriptors[fd] = &copy
	}
}

func truncateDescriptorInput(descriptor *descriptorTarget, filename string) {
	if descriptor == nil || descriptor.inputFile != filename {
		return
	}
	descriptor.input = newCertain[[]byte](nil)
}

func isExternalDevicePath(filename string) bool {
	return strings.HasPrefix(filename, "/dev/tcp/") || strings.HasPrefix(filename, "/dev/udp/")
}

func prepareOutputRedirection(s *State, filename string, appendMode bool) error {
	err := s.fs.write(filename, nil, appendMode)
	if errors.Is(err, iofs.ErrNotExist) {
		err = s.fs.writeWithParents(filename, nil, appendMode)
	}
	if err != nil {
		return outputRedirectionError(filename, err)
	}
	return nil
}

func outputRedirectionError(filename string, err error) error {
	switch {
	case errors.Is(err, iofs.ErrNotExist):
		return &redirectionFailure{message: fmt.Sprintf("%s: No such file or directory", filename)}
	case errors.Is(err, errIsDirectory):
		return &redirectionFailure{message: fmt.Sprintf("%s: Is a directory", filename)}
	case errors.Is(err, errNotDirectory):
		return &redirectionFailure{message: fmt.Sprintf("%s: Not a directory", filename)}
	default:
		return err
	}
}

func (e *ExecutionContext) redirectWord(s *State, word *syntax.Word) (string, bool, error) {
	expansion, err := e.expandFields(s, []*syntax.Word{word}, true)
	if err != nil {
		return "", false, fmt.Errorf("expand redirection: %w", err)
	}
	if expansion.hostUnknown || wordHasUnknownData(s, word) {
		return "", true, nil
	}
	if len(expansion.fields) != 1 {
		return "", false, &redirectionFailure{message: "ambiguous redirect"}
	}
	return expansion.fields[0], false, nil
}

func (e *ExecutionContext) redirectLiteralValue(s *State, word *syntax.Word) (string, bool, error) {
	if word == nil {
		return "", false, nil
	}
	certainty := wordCertainty(s, word)
	value, err := e.expandWordValue(s, word, expand.Literal)
	return value, certainty.hostUnknown() || certainty.dataUnknown(), err
}

func redirectFD(redirection *syntax.Redirect) (int, error) {
	if redirection.N == nil {
		switch redirection.Op {
		case syntax.RdrIn, syntax.RdrInOut, syntax.DplIn, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
			return 0, nil
		default:
			return 1, nil
		}
	}
	fd, err := strconv.Atoi(redirection.N.Value)
	if err != nil {
		return 0, fmt.Errorf("unsupported named file descriptor %q", redirection.N.Value)
	}
	return fd, nil
}
