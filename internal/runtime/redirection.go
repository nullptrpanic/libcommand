package runtime

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"strconv"
	"strings"

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
			return s.fs.writeValue(target.file, contents, target.append, unknown)
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
	stdoutTarget := plan.descriptors[1].output
	stderrTarget := plan.descriptors[2].output
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

func (e *ExecutionContext) inputOnlySubstitution(s *State, substitution *syntax.CmdSubst) (*substitutionResult, *substitutionFailure, bool, error) {
	if len(substitution.Stmts) != 1 {
		return nil, nil, false, nil
	}
	statement := substitution.Stmts[0]
	if len(statement.Redirs) != 1 || statement.Redirs[0].Op != syntax.RdrIn {
		return nil, nil, false, nil
	}
	if statement.Cmd != nil {
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) != 0 || len(call.Assigns) != 0 {
			return nil, nil, false, nil
		}
	}
	filename, unresolved, err := e.redirectWord(s, statement.Redirs[0].Word)
	if err != nil {
		return nil, nil, true, err
	}
	if unresolved || isExternalDevicePath(filename) {
		return &substitutionResult{
			stdout:     newUnresolved[[]byte](nil),
			exitStatus: newUnresolved(0),
		}, nil, true, nil
	}
	directory, _ := s.dir.Data()
	filename = s.fs.resolve(directory, filename)
	contents, unknown, exists := s.fs.readFile(filename)
	if !exists {
		if err := s.fs.writeWithParents(filename, nil, false); err != nil {
			return nil, nil, true, outputRedirectionError(filename, err)
		}
		return &substitutionResult{stdout: newCertain[[]byte](nil), exitStatus: newCertain(0)}, nil, true, nil
	}
	stdout := newCertain(contents)
	if unknown {
		stdout = newUnresolved(contents)
	}
	return &substitutionResult{stdout: stdout, exitStatus: newCertain(0)}, nil, true, nil
}

func (e *ExecutionContext) prepareRedirections(s *State, redirections []*syntax.Redirect) (*redirectionPlan, error) {
	plan := &redirectionPlan{
		descriptors: map[int]*descriptorTarget{
			0: {input: cloneUncertainBytes(s.stdin)},
			1: {output: &outputTarget{id: 1, captureFD: 1}},
			2: {output: &outputTarget{id: 2, captureFD: 2}},
		},
	}
	directory, _ := s.dir.Data()
	nextTargetID := 3
	for _, redirection := range redirections {
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
				target := &descriptorTarget{input: newUnresolved[[]byte](nil)}
				if redirection.Op == syntax.RdrInOut {
					target.output = &outputTarget{id: nextTargetID, external: true}
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
			if unknown {
				input = newUnresolved([]byte(value + "\n"))
			}
			plan.descriptors[fd] = &descriptorTarget{input: input}
			if fd == 0 {
				plan.stdinReplaced = true
			}
		case syntax.Hdoc, syntax.DashHdoc:
			value, unknown, err := e.redirectLiteralValue(s, redirection.Hdoc)
			if err != nil {
				return nil, fmt.Errorf("expand here-document: %w", err)
			}
			input := newCertain([]byte(value))
			if unknown {
				input = newUnresolved([]byte(value))
			}
			plan.descriptors[fd] = &descriptorTarget{input: input}
			if fd == 0 {
				plan.stdinReplaced = true
			}
		case syntax.RdrOut, syntax.RdrClob, syntax.AppOut:
			filename, unresolved, err := e.redirectWord(s, redirection.Word)
			if err != nil {
				return nil, err
			}
			appendMode := redirection.Op == syntax.AppOut
			var output *outputTarget
			if unresolved {
				output = &outputTarget{id: nextTargetID, external: true}
			} else {
				filename = s.fs.resolve(directory, filename)
				if isExternalDevicePath(filename) {
					output = &outputTarget{id: nextTargetID, external: true}
				} else {
					if prepareErr := prepareOutputRedirection(s, filename, appendMode); prepareErr != nil {
						return nil, prepareErr
					}
					output = &outputTarget{id: nextTargetID, file: filename, append: appendMode}
					if !appendMode {
						truncateDescriptorInput(plan.descriptors[0], filename)
					}
				}
			}
			nextTargetID++
			plan.descriptors[fd] = &descriptorTarget{output: output}
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Target: filename, Unresolved: unresolved})
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
				filename := s.fs.resolve(directory, target)
				var output *outputTarget
				if isExternalDevicePath(filename) {
					output = &outputTarget{id: nextTargetID, external: true}
				} else {
					if prepareErr := prepareOutputRedirection(s, filename, false); prepareErr != nil {
						return nil, prepareErr
					}
					output = &outputTarget{id: nextTargetID, file: filename}
					truncateDescriptorInput(plan.descriptors[0], filename)
				}
				nextTargetID++
				descriptor := &descriptorTarget{output: output}
				plan.descriptors[1] = descriptor
				plan.descriptors[2] = descriptor
				target = filename
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
			var output *outputTarget
			if unresolved {
				output = &outputTarget{id: nextTargetID, external: true}
			} else {
				filename = s.fs.resolve(directory, filename)
				if isExternalDevicePath(filename) {
					output = &outputTarget{id: nextTargetID, external: true}
				} else {
					if prepareErr := prepareOutputRedirection(s, filename, appendMode); prepareErr != nil {
						return nil, prepareErr
					}
					output = &outputTarget{id: nextTargetID, file: filename, append: appendMode}
					if !appendMode {
						truncateDescriptorInput(plan.descriptors[0], filename)
					}
				}
			}
			nextTargetID++
			descriptor := &descriptorTarget{output: output}
			plan.descriptors[1] = descriptor
			plan.descriptors[2] = descriptor
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Target: filename, Unresolved: unresolved})
		default:
			return nil, fmt.Errorf("unsupported redirection operator %v", redirection.Op)
		}
	}
	return plan, nil
}

func abstractDescriptor(targetID int) *descriptorTarget {
	return &descriptorTarget{
		input:  newUnresolved[[]byte](nil),
		output: &outputTarget{id: targetID, external: true},
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
