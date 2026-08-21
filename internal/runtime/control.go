package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type ExecutionContext struct {
	ctx                   context.Context
	config                Config
	candidates            *candidateIndex
	discoveryDepth        int
	statementDepth        int
	executedSteps         int
	errexitSuppressed     int
	errTrapSuppressed     int
	processSequence       int
	stop                  bool
	variableRollbacks     []*variableRollback
	variableRollbackBytes int
	nestedShellBytes      int
	backgroundStepLimit   int
	trace                 *executionTrace
	redirects             []*Redirect
}

type variableRollback struct {
	state             *State
	name              string
	value             expand.Variable
	exists            bool
	unknown           bool
	indexedSlots      map[int]struct{}
	version           uint64
	versionExists     bool
	materializedBytes int
}

type outcome struct {
	stdout        []byte
	stderr        []byte
	stdoutUnknown bool
	stderrUnknown bool
	status        Status
}

type pathResult struct {
	state  *State
	status Status
}

func (e *ExecutionContext) resolveExitStatus(path *pathResult, source *location) ([]*pathResult, error) {
	_, unresolved := path.state.exitStatus.Data()
	if !unresolved {
		return []*pathResult{path}, nil
	}
	failureState := path.state.exitFailure
	if failureState == nil {
		failureState = path.state
	}
	if status := e.reserveExecutionSteps(path.state, 1, source); status != StatusCompleted {
		path.status = status
		return []*pathResult{path}, nil
	}
	success := path.state.clone()
	success.setExitCode(0)
	paths := make([]*pathResult, 0, 2)
	paths = append(paths, &pathResult{state: success, status: path.status})
	failure := failureState.clone()
	failure.setExitCode(1)
	paths = append(paths, &pathResult{state: failure, status: path.status})
	pathID := e.trace.ensurePath(path.state)
	e.trace.assignSuccessorPaths(pathID, e.trace.currentNodeID(path.state), paths)
	if err := e.checkPathsMaterialization(paths, 0, source); err != nil {
		return paths, err
	}
	return paths, nil
}

type truthValue uint8

const (
	truthFalse truthValue = iota
	truthTrue
	truthUnknown
)

type redirectionPlan struct {
	descriptors   map[int]*descriptorTarget
	stdinReplaced bool
	redirects     []*Redirect
}

type descriptorTarget struct {
	input     *uncertain[[]byte]
	inputFile string
	output    *outputTarget
}

type outputTarget struct {
	id        int
	captureFD int
	file      string
	append    bool
	external  bool
}

// NewExecutionContext creates the mutable execution state for one simulation.
func NewExecutionContext(ctx context.Context, config *Config) *ExecutionContext {
	runtimeConfig := *config
	runtimeConfig.MaxMemoryBytes = normalizedMaxMemoryBytes(runtimeConfig.MaxMemoryBytes)
	return &ExecutionContext{ctx: ctx, config: runtimeConfig, trace: newExecutionTrace(runtimeConfig.Trace, runtimeConfig.TraceOptions)}
}

// Execute evaluates file using this simulation's context and configuration.
func (e *ExecutionContext) Execute(file *syntax.File, request *Request) (err error) {
	e.trace.start()
	defer func() {
		e.trace.finish(err)
	}()
	if err := e.ctx.Err(); err != nil {
		return err
	}
	if _, err := candidateIndexMaterialization(e.ctx, file, 0, e.config.MaxMemoryBytes); err != nil {
		return err
	}
	e.trace.discover(file, request.Source, "command.sh", 0)
	current, err := initializeState(request, e.config.MaxMemoryBytes)
	if err != nil {
		return err
	}
	candidates, err := buildCandidateIndex(e.ctx, file, commandCandidateLookup(e.config.LookupCommand))
	if err != nil {
		return err
	}
	e.candidates = candidates
	paths := []*pathResult{{state: current, status: StatusCompleted}}
	paths, err = e.evaluateStatements(paths, file.Stmts)
	if err != nil {
		e.trace.pathsCompleted(e, paths)
		return err
	}
	paths, err = e.evaluateExitTraps(paths)
	if err != nil {
		e.trace.pathsCompleted(e, paths)
		return err
	}
	e.trace.pathsCompleted(e, paths)
	if err := e.ctx.Err(); err != nil {
		return err
	}
	for _, result := range paths {
		status := result.status
		if status == StatusCompleted || status == StatusTerminated {
			continue
		}
		if result.state.issue != nil {
			return result.state.issue
		}
		return fmt.Errorf("simulation ended before completion with status %d", status)
	}
	return nil
}

// Execute evaluates file using a newly created execution context.
func Execute(ctx context.Context, file *syntax.File, request *Request, config *Config) error {
	return NewExecutionContext(ctx, config).Execute(file, request)
}

func (e *ExecutionContext) evaluateStatements(paths []*pathResult, statements []*syntax.Stmt) ([]*pathResult, error) {
	topLevel := e.statementDepth == 0
	e.statementDepth++
	defer func() {
		e.statementDepth--
		if topLevel {
			e.discardVariableRollbacks(0)
			e.variableRollbacks = nil
		}
	}()
	current := paths
	budget, budgetErr := e.newRetainedPathBudget(current)
	if budgetErr != nil {
		return current, e.failPaths(current, budgetErr, unknownLocation)
	}
	if budgetErr = e.checkRetainedPathBudget(budget, 0); budgetErr != nil {
		return current, e.failPaths(current, budgetErr, unknownLocation)
	}
	for _, statement := range statements {
		rollbackCheckpoint := len(e.variableRollbacks)
		next := make([]*pathResult, 0, len(current))
		for index, currentPath := range current {
			if currentPath.status != StatusCompleted {
				next = append(next, currentPath)
				continue
			}
			if currentPath.state.signal != signalNone {
				next = append(next, currentPath)
				continue
			}
			if e.stop {
				currentPath.status = StatusTerminated
				next = append(next, currentPath)
				continue
			}
			previousBytes, previousRetained, ok := retainedPathStateBytes(currentPath)
			if !ok {
				return next, e.failMaterializationGroups([][]*pathResult{next, current[index:]}, sourceLocation(statement))
			}
			pathID := e.trace.statementStarted(e, currentPath.state, statement, budget)
			successors, err := e.evaluateStatement(currentPath.state, statement)
			if err != nil {
				next = append(next, successors...)
				return next, err
			}
			suppressErrTrap := statementSuppressesParentErrTrap(statement)
			if suppressErrTrap {
				e.errTrapSuppressed++
			}
			successors, err = e.applyFailureEffects(successors, sourceLocation(statement))
			if suppressErrTrap {
				e.errTrapSuppressed--
			}
			if err == nil {
				successors, err = e.resolveCorrelatedExitStatuses(successors, sourceLocation(statement))
			}
			if err == nil {
				if contextErr := e.ctx.Err(); contextErr != nil {
					for _, successor := range successors {
						if successor.status != StatusCompleted {
							continue
						}
						e.setIssue(successor.state, contextErr, sourceLocation(statement))
						successor.status = StatusIncomplete
					}
					err = contextErr
				}
			}
			next = append(next, successors...)
			if err != nil {
				return next, err
			}
			e.trace.assignSuccessorPaths(pathID, e.trace.nodeID(statement), successors)
			if !budget.replace(previousBytes, previousRetained, successors) {
				e.rollbackVariables(rollbackCheckpoint)
				groups := [][]*pathResult{next, current[index+1:]}
				return next, e.failMaterializationGroups(groups, sourceLocation(statement))
			}
			if err := e.checkRetainedPathBudget(budget, 0); err != nil {
				e.rollbackVariables(rollbackCheckpoint)
				groups := [][]*pathResult{next, current[index+1:]}
				return next, e.failPathGroups(groups, err, sourceLocation(statement))
			}
			e.trace.statementFinished(e, statement, successors, budget)
		}
		current = next
		if topLevel {
			e.discardVariableRollbacks(rollbackCheckpoint)
		}
	}
	return current, nil
}

func (e *ExecutionContext) resolveCorrelatedExitStatuses(paths []*pathResult, source *location) ([]*pathResult, error) {
	result := make([]*pathResult, 0, len(paths))
	for _, path := range paths {
		_, exitUnresolved := path.state.exitStatus.Data()
		if path.status != StatusCompleted || !exitUnresolved || path.state.exitFailure == nil {
			result = append(result, path)
			continue
		}
		resolved, err := e.resolveExitStatus(path, source)
		result = append(result, resolved...)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func (e *ExecutionContext) recordVariableRollback(s *State, name string) {
	if e.statementDepth == 0 {
		return
	}
	value, exists := s.vars.data[name]
	version, versionExists := s.vars.versions[name]
	rollback := &variableRollback{
		state:         s,
		name:          name,
		value:         value,
		exists:        exists,
		unknown:       s.vars.isUnknown(name),
		indexedSlots:  s.vars.indexedSlots(name),
		version:       version,
		versionExists: versionExists,
	}
	rollback.materializedBytes = addRetainedBytes(materialize.EntryBytes+len(name), variableValueBytes(&value))
	if rollback.indexedSlots != nil {
		rollback.materializedBytes = addRetainedBytes(rollback.materializedBytes, indexedSlotsBytes(name, rollback.indexedSlots))
	}
	e.variableRollbackBytes = addRetainedBytes(e.variableRollbackBytes, rollback.materializedBytes)
	e.variableRollbacks = append(e.variableRollbacks, rollback)
}

func (e *ExecutionContext) rollbackVariables(checkpoint int) {
	for index := len(e.variableRollbacks) - 1; index >= checkpoint; index-- {
		rollback := e.variableRollbacks[index]
		if rollback.exists {
			rollback.state.vars.putIndexedWithCertainty(rollback.name, rollback.value, rollback.unknown, rollback.indexedSlots)
		} else {
			rollback.state.vars.delete(rollback.name)
		}
		if rollback.versionExists {
			rollback.state.vars.versions[rollback.name] = rollback.version
		} else if _, exists := rollback.state.vars.versions[rollback.name]; exists {
			delete(rollback.state.vars.versions, rollback.name)
			rollback.state.vars.materializedBytes -= materialize.EntryBytes + len(rollback.name)
		}
	}
	e.discardVariableRollbacks(checkpoint)
}

func (e *ExecutionContext) discardVariableRollbacks(checkpoint int) {
	for index := checkpoint; index < len(e.variableRollbacks); index++ {
		e.variableRollbackBytes -= e.variableRollbacks[index].materializedBytes
		e.variableRollbacks[index] = nil
	}
	e.variableRollbacks = e.variableRollbacks[:checkpoint]
}

func statementSuppressesParentErrTrap(statement *syntax.Stmt) bool {
	_, subshell := statement.Cmd.(*syntax.Subshell)
	return subshell
}

func (e *ExecutionContext) evaluateStatement(s *State, statement *syntax.Stmt) ([]*pathResult, error) {
	if e.candidates.contains(statement) {
		e.discoveryDepth++
		defer func() { e.discoveryDepth-- }()
	}
	s.pushSubstitutionFrame()
	paths, err := e.evaluateStatementInFrameFully(s, statement)
	if err == nil {
		paths, err = e.evaluateProcessOutputs(paths)
	}
	if err == nil && len(paths) > 1 {
		err = e.checkPathsMaterialization(paths, 0, sourceLocation(statement))
	}
	visited := make(map[*State]struct{})
	for _, path := range paths {
		popSubstitutionFrameTree(path.state, visited)
	}
	return paths, err
}

func (e *ExecutionContext) evaluateStatementInFrameFully(s *State, statement *syntax.Stmt) ([]*pathResult, error) {
	pending := []*pathResult{{state: s, status: StatusCompleted}}
	results := make([]*pathResult, 0, 1)
	budget, err := e.newRetainedPathBudget(pending)
	if err != nil {
		return pending, e.failPaths(pending, err, sourceLocation(statement))
	}
	if err := e.checkRetainedPathBudget(budget, 0); err != nil {
		return pending, e.failPaths(pending, err, sourceLocation(statement))
	}

	for len(pending) != 0 {
		last := len(pending) - 1
		current := pending[last]
		pending[last] = nil
		pending = pending[:last]
		previousBytes, previousRetained, ok := retainedPathStateBytes(current)
		if !ok {
			groups := [][]*pathResult{results, pending, []*pathResult{current}}
			return results, e.failMaterializationGroups(groups, sourceLocation(statement))
		}

		paths, evaluationErr := e.evaluateStatementInFrame(current.state, statement)
		continuation := false
		if request, requested := requestedSubstitution(evaluationErr); requested {
			paths, evaluationErr = e.evaluateSubstitutionPaths(request.state, request.substitution)
			continuation = true
		} else if request, requested := requestedProcessSubstitution(evaluationErr); requested {
			paths, evaluationErr = e.evaluateProcessSubstitutionPaths(request.state, request.substitution)
			continuation = true
		}

		if !budget.replace(previousBytes, previousRetained, paths) {
			groups := [][]*pathResult{results, pending, paths}
			return append(results, paths...), e.failMaterializationGroups(groups, sourceLocation(statement))
		}
		if err := e.checkRetainedPathBudget(budget, 0); err != nil {
			groups := [][]*pathResult{results, pending, paths}
			return append(results, paths...), e.failPathGroups(groups, err, sourceLocation(statement))
		}
		if evaluationErr != nil {
			return append(results, paths...), evaluationErr
		}
		if !continuation {
			results = append(results, paths...)
			continue
		}

		for index := len(paths) - 1; index >= 0; index-- {
			path := paths[index]
			if path.status == StatusCompleted {
				pending = append(pending, path)
				continue
			}
			results = append(results, path)
		}
	}
	return results, nil
}

func (e *ExecutionContext) evaluateStatementInFrame(s *State, statement *syntax.Stmt) ([]*pathResult, error) {
	if err := e.ctx.Err(); err != nil {
		e.setIssue(s, err, sourceLocation(statement))
		return []*pathResult{{state: s, status: StatusIncomplete}}, err
	}
	if statement.Coprocess || statement.Disown {
		result := e.unresolved(s, fmt.Sprintf("unsupported asynchronous Bash statement %T", statement.Cmd), sourceLocation(statement))
		return []*pathResult{{state: s, status: result.status}}, nil
	}
	if statement.Background {
		return e.evaluateBackground(s, statement)
	}
	if statement.Negated {
		positive := *statement
		positive.Negated = false
		paths, err := e.evaluateStatementWithoutErrexit(s, &positive)
		if err == nil {
			paths, err = e.resolveCorrelatedExitStatuses(paths, sourceLocation(statement))
		}
		for index := range paths {
			exitCode, exitUnresolved := paths[index].state.exitStatus.Data()
			if paths[index].status == StatusCompleted && !exitUnresolved {
				pipelineStatuses, pipelineUnknown := paths[index].state.pipelineStatusValues()
				paths[index].state.setExitCode(boolExitCode(exitCode != 0))
				paths[index].state.setPipelineStatusValues(pipelineStatuses, pipelineUnknown)
			}
		}
		return paths, err
	}
	if len(statement.Redirs) != 0 {
		return e.evaluateRedirectedStatement(s, statement)
	}
	if statement.Cmd == nil {
		return []*pathResult{{state: s, status: StatusCompleted}}, nil
	}
	if call, ok := statement.Cmd.(*syntax.CallExpr); ok {
		return e.evaluateCallStatement(s, call)
	}
	switch command := statement.Cmd.(type) {
	case *syntax.IfClause:
		return e.evaluateIf(s, command)
	case *syntax.BinaryCmd:
		if command.Op == syntax.AndStmt || command.Op == syntax.OrStmt {
			return e.evaluateLogical(s, command)
		}
		if command.Op == syntax.Pipe || command.Op == syntax.PipeAll {
			return e.evaluatePipeline(s, command)
		}
	case *syntax.TestClause:
		return e.evaluateTest(s, command)
	case *syntax.CaseClause:
		return e.evaluateCase(s, command)
	case *syntax.WhileClause:
		return e.evaluateWhile(s, command)
	case *syntax.ForClause:
		return e.evaluateFor(s, command)
	case *syntax.ArithmCmd:
		return e.evaluateArithmetic(s, command)
	case *syntax.DeclClause:
		return e.evaluateDeclarationCommand(s, command)
	case *syntax.LetClause:
		return e.evaluateLetCommand(s, command)
	case *syntax.FuncDecl:
		if command.Name == nil {
			result := e.unresolved(s, "anonymous function declaration is not supported", sourceLocation(command))
			return []*pathResult{{state: s, status: result.status}}, nil
		}
		s.setFunction(command.Name.Value, command)
		s.setExitCode(0)
		return []*pathResult{{state: s, status: StatusCompleted}}, nil
	case *syntax.TimeClause:
		if command.Stmt == nil {
			s.setExitCode(0)
			return []*pathResult{{state: s, status: StatusCompleted}}, nil
		}
		return e.evaluateStatement(s, command.Stmt)
	case *syntax.Subshell:
		return e.evaluateSubshell(s, command)
	case *syntax.Block:
		return e.evaluateStatements([]*pathResult{{state: s, status: StatusCompleted}}, command.Stmts)
	}

	result := e.unresolved(s, fmt.Sprintf("unsupported Bash command %T", statement.Cmd), sourceLocation(statement))
	return []*pathResult{{state: s, status: result.status}}, nil
}

func (e *ExecutionContext) evaluateRedirectedStatement(s *State, statement *syntax.Stmt) ([]*pathResult, error) {
	plan, err := e.prepareRedirections(s, statement.Redirs)
	if err != nil {
		if expansionRequested(err) {
			return nil, err
		}
		if result, resultErr, incomplete := e.incompleteFromEvaluationError(s, err, sourceLocation(statement)); incomplete {
			return []*pathResult{{state: s, status: result.status}}, resultErr
		}
		var failure *redirectionFailure
		if errors.As(err, &failure) {
			s.setExitCode(1)
			status := e.appendStreams(s, nil, []byte(failure.Error()+"\n"), false, false, sourceLocation(statement))
			return []*pathResult{{state: s, status: status}}, nil
		}
		result := e.unresolved(s, err.Error(), sourceLocation(statement))
		return []*pathResult{{state: s, status: result.status}}, nil
	}
	originalStdin := s.stdin
	originalStdout := s.stdout
	originalStderr := s.stderr
	stdoutData, _ := originalStdout.Data()
	stderrData, _ := originalStderr.Data()
	stdoutPrefix := len(stdoutData)
	stderrPrefix := len(stderrData)
	stdin := plan.descriptors[0].input
	if stdin == nil {
		stdin = newCertain[[]byte](nil)
	}
	s.stdin = cloneUncertainBytes(stdin)
	s.stdout = newCertain(stdoutData)
	s.stderr = newCertain(stderrData)
	unredirected := *statement
	unredirected.Redirs = nil
	redirectCheckpoint := len(e.redirects)
	e.redirects = append(e.redirects, plan.redirects...)
	defer func() {
		e.redirects = e.redirects[:redirectCheckpoint]
	}()
	paths, evaluationErr := e.evaluateStatement(s, &unredirected)
	for index := range paths {
		path := paths[index]
		pathStdout, stdoutUnresolved := path.state.stdout.Data()
		pathStderr, stderrUnresolved := path.state.stderr.Data()
		stdout := append([]byte(nil), pathStdout[stdoutPrefix:]...)
		stderr := append([]byte(nil), pathStderr[stderrPrefix:]...)
		path.state.stdout = originalStdout
		path.state.stderr = originalStderr
		stdout, stderr, stdoutUnresolved, stderrUnresolved, outputErr := e.applyOutputTargets(path.state, plan, stdout, stderr, stdoutUnresolved, stderrUnresolved)
		if outputErr != nil {
			e.setIssue(path.state, outputErr, sourceLocation(statement))
			path.status = StatusIncomplete
			if plan.stdinReplaced {
				path.state.stdin = originalStdin
			}
			continue
		}
		if status := e.appendStreams(path.state, stdout, stderr, stdoutUnresolved, stderrUnresolved, sourceLocation(statement)); status != StatusCompleted {
			path.status = status
		}
		if plan.stdinReplaced {
			path.state.stdin = originalStdin
		}
	}
	return paths, evaluationErr
}

func (e *ExecutionContext) evaluatePipeline(s *State, pipeline *syntax.BinaryCmd) ([]*pathResult, error) {
	left := s.clone()
	left.stdin = s.stdin
	left.resetOutput()
	leftPaths, err := e.evaluateStatementWithoutErrexit(left, pipeline.X)
	if err == nil {
		leftPaths, err = e.resolveCorrelatedExitStatuses(leftPaths, sourceLocation(pipeline.X))
	}
	if err != nil {
		return leftPaths, err
	}

	results := make([]*pathResult, 0, len(leftPaths))
	for _, leftPath := range leftPaths {
		if leftPath.status != StatusCompleted || e.stop {
			parent := s.clone()
			mergeIssue(parent, leftPath.state)
			mergeChildInput(parent, leftPath.state)
			status := leftPath.status
			leftStdout, stdoutUnresolved := leftPath.state.stdout.Data()
			leftStderr, stderrUnresolved := leftPath.state.stderr.Data()
			if outputStatus := e.appendStreams(parent, leftStdout, leftStderr, stdoutUnresolved, stderrUnresolved, sourceLocation(pipeline)); outputStatus != StatusCompleted {
				status = outputStatus
			}
			exitCode, exitUnresolved := leftPath.state.exitStatus.Data()
			parent.setExitStatus(exitCode, exitUnresolved)
			if e.stop && status == StatusCompleted {
				status = StatusTerminated
			}
			results = append(results, &pathResult{state: parent, status: status})
			if err := e.checkPathsMaterialization(results, 0, sourceLocation(pipeline)); err != nil {
				return results, err
			}
			continue
		}

		leftStdout, pipeInputUnresolved := leftPath.state.stdout.Data()
		leftStderrData, leftStderrUnresolved := leftPath.state.stderr.Data()
		pipeInput := append([]byte(nil), leftStdout...)
		leftStderr := append([]byte(nil), leftStderrData...)
		if pipeline.Op == syntax.PipeAll {
			pipeInput = append(pipeInput, leftStderr...)
			pipeInputUnresolved = pipeInputUnresolved || leftStderrUnresolved
			leftStderr = nil
			leftStderrUnresolved = false
		}
		right := s.clone()
		mergeIssue(right, leftPath.state)
		right.fs = leftPath.state.fs.clone()
		if pipeInputUnresolved {
			right.stdin = newUnresolved(pipeInput)
		} else {
			right.stdin = newCertain(pipeInput)
		}
		right.resetOutput()
		rightInput := &pathResult{state: right, status: StatusCompleted}
		if err := e.checkPathGroupsMaterialization(0, sourceLocation(pipeline), results, []*pathResult{rightInput}); err != nil {
			return append(results, rightInput), err
		}
		rightPaths, rightErr := e.evaluateStatementWithoutErrexit(right, pipeline.Y)
		if rightErr == nil {
			rightPaths, rightErr = e.resolveCorrelatedExitStatuses(rightPaths, sourceLocation(pipeline.Y))
		}
		for _, rightPath := range rightPaths {
			parent := s.clone()
			mergeIssue(parent, rightPath.state)
			mergeChildInput(parent, leftPath.state)
			parent.fs = rightPath.state.fs.clone()
			rightStdout, rightStdoutUnresolved := rightPath.state.stdout.Data()
			rightStderr, rightStderrUnresolved := rightPath.state.stderr.Data()
			combinedStderr := append(append([]byte(nil), leftStderr...), rightStderr...)
			combinedStderrUnresolved := leftStderrUnresolved || rightStderrUnresolved
			status := rightPath.status
			if outputStatus := e.appendStreams(parent, rightStdout, combinedStderr, rightStdoutUnresolved, combinedStderrUnresolved, sourceLocation(pipeline)); outputStatus != StatusCompleted {
				status = outputStatus
			}
			rightExitCode, rightExitUnresolved := rightPath.state.exitStatus.Data()
			parent.setExitStatus(rightExitCode, rightExitUnresolved)
			if s.options.pipefail && !rightExitUnresolved && rightExitCode == 0 {
				leftExitCode, leftExitUnresolved := leftPath.state.exitStatus.Data()
				parent.setExitStatus(leftExitCode, leftExitUnresolved)
			}
			leftStatuses, leftUnknown := leftPath.state.pipelineStatusValues()
			rightStatuses, rightUnknown := rightPath.state.pipelineStatusValues()
			parent.setPipelineStatusValues(append(leftStatuses, rightStatuses...), leftUnknown || rightUnknown)
			results = append(results, &pathResult{state: parent, status: status})
		}
		if rightErr != nil {
			return results, rightErr
		}
		if err := e.checkPathsMaterialization(results, 0, sourceLocation(pipeline)); err != nil {
			return results, err
		}
	}
	return results, nil
}

func (e *ExecutionContext) evaluateProcessSubstitutionPaths(s *State, substitution *syntax.ProcSubst) ([]*pathResult, error) {
	path := e.nextProcessPath(s)
	if substitution.Op == syntax.CmdOut {
		if err := s.fs.write(path, nil, false); err != nil {
			e.setIssue(s, err, sourceLocation(substitution))
			return []*pathResult{{state: s, status: StatusIncomplete}}, nil
		}
		s.setProcessSubstitution(substitution, path, true)
		return []*pathResult{{state: s, status: StatusCompleted}}, nil
	}
	if substitution.Op != syntax.CmdIn && substitution.Op != syntax.CmdInTemp {
		return e.unresolvedPath(s, fmt.Sprintf("unsupported process substitution operator %s", substitution.Op), sourceLocation(substitution)), nil
	}

	child := s.clone()
	clearInheritedExitTrap(child)
	if status := e.checkContext(child, sourceLocation(substitution)); status != StatusCompleted {
		return []*pathResult{{state: child, status: status}}, nil
	}
	child.resetOutput()
	childPaths, err := e.evaluateStatements([]*pathResult{{state: child, status: StatusCompleted}}, substitution.Stmts)
	childPaths, trapErr := e.evaluateExitTraps(childPaths)
	if err == nil {
		err = trapErr
	}
	results := make([]*pathResult, 0, len(childPaths))
	for _, childPath := range childPaths {
		parent := s.clone()
		mergeIssue(parent, childPath.state)
		mergeChildInput(parent, childPath.state)
		parent.fs = childPath.state.fs.clone()
		status := childPath.status
		childStderr, stderrUnresolved := childPath.state.stderr.Data()
		if outputStatus := e.appendStreams(parent, nil, childStderr, false, stderrUnresolved, sourceLocation(substitution)); outputStatus != StatusCompleted {
			status = outputStatus
		}
		if status != StatusCompleted {
			results = append(results, &pathResult{state: parent, status: status})
			continue
		}
		childStdout, stdoutUnresolved := childPath.state.stdout.Data()
		if err := parent.fs.writeValue(path, childStdout, false, stdoutUnresolved); err != nil {
			e.setIssue(parent, err, sourceLocation(substitution))
			results = append(results, &pathResult{state: parent, status: StatusIncomplete})
			continue
		}
		parent.setProcessSubstitution(substitution, path, false)
		results = append(results, &pathResult{state: parent, status: StatusCompleted})
	}
	return results, err
}

func (e *ExecutionContext) evaluateProcessOutputs(paths []*pathResult) ([]*pathResult, error) {
	current := paths
	for {
		found := false
		next := make([]*pathResult, 0, len(current))
		for _, outer := range current {
			outputs := outer.state.takeProcessOutputs()
			if len(outputs) == 0 || outer.status != StatusCompleted {
				next = append(next, outer)
				continue
			}
			found = true
			names := make([]string, 0, len(outputs))
			for name := range outputs {
				names = append(names, name)
			}
			sort.Strings(names)
			active := []*pathResult{outer}
			for _, name := range names {
				consumed := make([]*pathResult, 0, len(active))
				for _, currentPath := range active {
					child := currentPath.state.clone()
					clearInheritedExitTrap(child)
					if status := e.checkContext(child, sourceLocation(outputs[name])); status != StatusCompleted {
						parent := currentPath.state.clone()
						mergeIssue(parent, child)
						consumed = append(consumed, &pathResult{state: parent, status: status})
						continue
					}
					input, inputUnresolved := child.fs.readValue(name)
					if inputUnresolved {
						child.stdin = newUnresolved(input)
					} else {
						child.stdin = newCertain(input)
					}
					child.resetOutput()
					childPaths, err := e.evaluateStatements([]*pathResult{{state: child, status: StatusCompleted}}, outputs[name].Stmts)
					childPaths, trapErr := e.evaluateExitTraps(childPaths)
					if err == nil {
						err = trapErr
					}
					for _, childPath := range childPaths {
						parent := currentPath.state.clone()
						mergeIssue(parent, childPath.state)
						parent.fs = childPath.state.fs.clone()
						status := currentPath.status
						if childPath.status != StatusCompleted {
							status = childPath.status
						}
						childStdout, stdoutUnresolved := childPath.state.stdout.Data()
						childStderr, stderrUnresolved := childPath.state.stderr.Data()
						if outputStatus := e.appendStreams(parent, childStdout, childStderr, stdoutUnresolved, stderrUnresolved, sourceLocation(outputs[name])); outputStatus != StatusCompleted {
							status = outputStatus
						}
						consumed = append(consumed, &pathResult{state: parent, status: status})
					}
					if err != nil {
						return append(next, consumed...), err
					}
				}
				active = consumed
			}
			next = append(next, active...)
		}
		current = next
		if !found {
			return current, nil
		}
	}
}

func (e *ExecutionContext) nextProcessPath(s *State) string {
	for {
		e.processSequence++
		name := fmt.Sprintf("/.libcommand-process-%d", e.processSequence)
		if _, exists := s.fs.files[name]; exists {
			continue
		}
		if _, exists := s.fs.dirs[name]; exists {
			continue
		}
		return name
	}
}
