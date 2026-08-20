package runtime

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"strconv"

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
	if plan.stdout.id == plan.stderr.id {
		combined := append(append([]byte(nil), stdout...), stderr...)
		if err := write(&plan.stdout, combined, stdoutUnknown || stderrUnknown); err != nil {
			return nil, nil, false, false, err
		}
	} else {
		if err := write(&plan.stdout, stdout, stdoutUnknown); err != nil {
			return nil, nil, false, false, err
		}
		if err := write(&plan.stderr, stderr, stderrUnknown); err != nil {
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
	filename, err := e.redirectWord(s, statement.Redirs[0].Word)
	if err != nil {
		return nil, nil, true, err
	}
	directory, _ := s.dir.Data()
	filename = s.fs.resolve(directory, filename)
	contents, unknown, exists := s.fs.readFile(filename)
	if !exists {
		if e.discoveryDepth > 0 {
			failure := s.snapshotForUnknownFailure()
			if err := s.fs.writeAbstract(filename, nil, false, true); err == nil {
				return &substitutionResult{
					stdout:     newUnresolved[[]byte](nil),
					exitStatus: newUnresolved(0),
				}, &substitutionFailure{state: failure, stdout: newCertain[[]byte](nil)}, true, nil
			}
		}
		return nil, nil, true, &redirectionFailure{message: fmt.Sprintf("%s: No such file or directory", filename)}
	}
	stdout := newCertain(contents)
	if unknown {
		stdout = newUnresolved(contents)
	}
	return &substitutionResult{stdout: stdout, exitStatus: newCertain(0)}, nil, true, nil
}

func (e *ExecutionContext) prepareRedirections(s *State, redirections []*syntax.Redirect) (*redirectionPlan, error) {
	plan := &redirectionPlan{
		stdin:  cloneUncertainBytes(s.stdin),
		stdout: outputTarget{id: 1, captureFD: 1},
		stderr: outputTarget{id: 2, captureFD: 2},
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
			if fd != 0 {
				return nil, fmt.Errorf("unsupported input file descriptor %d", fd)
			}
			filename, err := e.redirectWord(s, redirection.Word)
			if err != nil {
				return nil, err
			}
			filename = s.fs.resolve(directory, filename)
			contents, unknown, exists := s.fs.readFile(filename)
			if !exists {
				if redirection.Op != syntax.RdrInOut {
					if e.discoveryDepth == 0 {
						return nil, &redirectionFailure{message: fmt.Sprintf("%s: No such file or directory", filename)}
					}
					plan.failureStates = append(plan.failureStates, s.snapshotForUnknownFailure())
					if err := s.fs.writeAbstract(filename, nil, false, true); err != nil {
						return nil, outputRedirectionError(filename, err)
					}
					unknown = true
				} else if err := s.fs.write(filename, nil, false); err != nil {
					if e.discoveryDepth == 0 || !errors.Is(err, iofs.ErrNotExist) {
						return nil, outputRedirectionError(filename, err)
					}
					plan.failureStates = append(plan.failureStates, s.snapshotForUnknownFailure())
					if err := s.fs.writeAbstract(filename, nil, false, false); err != nil {
						return nil, outputRedirectionError(filename, err)
					}
				}
			}
			if unknown {
				plan.stdin = newUnresolved(contents)
			} else {
				plan.stdin = newCertain(contents)
			}
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Target: filename})
			plan.stdinReplaced = true
			plan.stdinFile = filename
		case syntax.WordHdoc:
			if fd != 0 {
				return nil, fmt.Errorf("unsupported here-string file descriptor %d", fd)
			}
			value, unknown, err := e.literalValueWithCertainty(s, redirection.Word)
			if err != nil {
				return nil, fmt.Errorf("expand here-string redirection: %w", err)
			}
			plan.stdin = newCertain([]byte(value + "\n"))
			if unknown {
				plan.stdin = newUnresolved([]byte(value + "\n"))
			}
			plan.stdinReplaced = true
			plan.stdinFile = ""
		case syntax.Hdoc, syntax.DashHdoc:
			if fd != 0 {
				return nil, fmt.Errorf("unsupported here-document file descriptor %d", fd)
			}
			value, unknown, err := e.literalValueWithCertainty(s, redirection.Hdoc)
			if err != nil {
				return nil, fmt.Errorf("expand here-document: %w", err)
			}
			plan.stdin = newCertain([]byte(value))
			if unknown {
				plan.stdin = newUnresolved([]byte(value))
			}
			plan.stdinReplaced = true
			plan.stdinFile = ""
		case syntax.RdrOut, syntax.RdrClob, syntax.AppOut:
			if fd != 1 && fd != 2 {
				return nil, fmt.Errorf("unsupported output file descriptor %d", fd)
			}
			filename, err := e.redirectWord(s, redirection.Word)
			if err != nil {
				return nil, err
			}
			filename = s.fs.resolve(directory, filename)
			failure, err := e.prepareOutputRedirection(s, filename, redirection.Op == syntax.AppOut)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				plan.failureStates = append(plan.failureStates, failure)
			}
			if redirection.Op != syntax.AppOut && filename == plan.stdinFile {
				plan.stdin = newCertain[[]byte](nil)
			}
			target := outputTarget{id: nextTargetID, file: filename, append: redirection.Op == syntax.AppOut}
			nextTargetID++
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Target: filename})
			if fd == 1 {
				plan.stdout = target
			} else {
				plan.stderr = target
			}
		case syntax.DplOut:
			if fd != 1 && fd != 2 {
				return nil, fmt.Errorf("unsupported duplicated output file descriptor %d", fd)
			}
			target, err := e.redirectWord(s, redirection.Word)
			if err != nil {
				return nil, err
			}
			var output outputTarget
			switch target {
			case "1":
				output = plan.stdout
			case "2":
				output = plan.stderr
			case "-":
				output = outputTarget{id: nextTargetID, file: "/dev/null"}
				nextTargetID++
			default:
				return nil, fmt.Errorf("unsupported output descriptor duplication %d>&%s", fd, target)
			}
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Target: target})
			if fd == 1 {
				plan.stdout = output
			} else {
				plan.stderr = output
			}
		case syntax.DplIn:
			if fd != 0 {
				return nil, fmt.Errorf("unsupported duplicated input file descriptor %d", fd)
			}
			target, err := e.redirectWord(s, redirection.Word)
			if err != nil {
				return nil, err
			}
			if target != "0" && target != "-" {
				return nil, fmt.Errorf("unsupported input descriptor duplication %d<&%s", fd, target)
			}
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Target: target})
			if target == "-" {
				plan.stdin = newCertain[[]byte](nil)
				plan.stdinReplaced = true
				plan.stdinFile = ""
			}
		case syntax.RdrAll, syntax.AppAll:
			filename, err := e.redirectWord(s, redirection.Word)
			if err != nil {
				return nil, err
			}
			filename = s.fs.resolve(directory, filename)
			failure, err := e.prepareOutputRedirection(s, filename, redirection.Op == syntax.AppAll)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				plan.failureStates = append(plan.failureStates, failure)
			}
			if redirection.Op != syntax.AppAll && filename == plan.stdinFile {
				plan.stdin = newCertain[[]byte](nil)
			}
			output := outputTarget{id: nextTargetID, file: filename, append: redirection.Op == syntax.AppAll}
			nextTargetID++
			plan.redirects = append(plan.redirects, &Redirect{FD: fd, Operator: redirection.Op.String(), Target: filename})
			plan.stdout = output
			plan.stderr = output
		default:
			return nil, fmt.Errorf("unsupported redirection operator %v", redirection.Op)
		}
	}
	return plan, nil
}

func (e *ExecutionContext) prepareOutputRedirection(s *State, filename string, appendMode bool) (*State, error) {
	err := s.fs.write(filename, nil, appendMode)
	if err == nil {
		return nil, nil
	}
	if e.discoveryDepth == 0 || !errors.Is(err, iofs.ErrNotExist) {
		return nil, outputRedirectionError(filename, err)
	}
	failure := s.snapshotForUnknownFailure()
	if err := s.fs.writeAbstract(filename, nil, appendMode, false); err != nil {
		return nil, outputRedirectionError(filename, err)
	}
	return failure, nil
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

func (e *ExecutionContext) redirectWord(s *State, word *syntax.Word) (string, error) {
	words, err := e.expandWords(s, []*syntax.Word{word})
	if err != nil {
		return "", fmt.Errorf("expand redirection: %w", err)
	}
	if len(words) != 1 {
		return "", fmt.Errorf("redirection expanded to %d fields", len(words))
	}
	if wordHasUnknownData(s, word) {
		return "", newUnknownValueError("redirection path")
	}
	return words[0], nil
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
