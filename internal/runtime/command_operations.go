package runtime

import (
	"fmt"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/expand"
)

// ShellProgram describes one child shell after its command-line options have
// been parsed by the bash or sh builtin.
type ShellProgram struct {
	Source    string
	Name      string
	Arguments []string
	Options   map[string]bool
	ParseOnly bool
}

// Invoke dispatches a command without consulting shell functions. When
// builtinOnly is true, the selected exact definition must be a shell builtin.
func (c *CommandContext) executeInvoke(name string, arguments []*Argument, builtinOnly bool) ([]*pathResult, error) {
	definition := c.execution.lookupCommandDefinition(name)
	if !commandDefinitionExecutable(definition) || definition.Fallback && builtinOnly || builtinOnly && !definition.Builtin {
		if builtinOnly {
			return c.applyOrdinaryResult(&CommandResult{
				Stderr:   []byte(fmt.Sprintf("builtin: %s: not a shell builtin\n", name)),
				ExitCode: 1,
			})
		}
		return c.applyOrdinaryResult(nil)
	}
	input, _ := c.state.stdin.Data()
	invocation, status, err := c.execution.commandInvocation(
		c.state,
		name,
		arguments,
		input,
		c.source,
		definition.Builtin && !definition.UserOverride,
	)
	if err != nil || status != StatusCompleted {
		return []*pathResult{{state: c.state, status: status}}, err
	}
	return c.execution.invokeCommand(c.state, c.source, nil, definition.Command, invocation)
}

// InvokeExternal dispatches only non-builtin definitions, as env does.
func (c *CommandContext) executeInvokeExternal(name string, arguments []*Argument) ([]*pathResult, error) {
	definition := c.execution.lookupCommandDefinition(name)
	if !commandDefinitionExecutable(definition) || definition.Builtin && !definition.UserOverride {
		return c.applyOrdinaryResult(nil)
	}
	return c.executeInvoke(name, arguments, false)
}

// InvokeWithEnvironment invokes a command with temporary exported variable
// changes and restores those variables on every returned path.
func (c *CommandContext) executeInvokeWithEnvironment(name string, arguments []*Argument, clear bool, unset []string, assignments map[string]string) ([]*pathResult, error) {
	saved := make(map[string]*savedVariable)
	state := c.state
	if clear {
		for _, variableName := range state.vars.names() {
			value := state.vars.Get(variableName)
			if !value.Exported {
				continue
			}
			saveEnvironmentVariable(state, saved, variableName)
			state.vars.delete(variableName)
		}
	}
	for _, variableName := range unset {
		saveEnvironmentVariable(state, saved, variableName)
		state.vars.delete(variableName)
	}
	for variableName, value := range assignments {
		saveEnvironmentVariable(state, saved, variableName)
		state.vars.put(variableName, expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: value})
	}
	paths, err := c.executeInvokeExternal(name, arguments)
	restoreTemporaryAssignments(paths, saved)
	return paths, err
}

func saveEnvironmentVariable(state *State, saved map[string]*savedVariable, name string) {
	if _, exists := saved[name]; exists {
		return
	}
	value, exists := state.vars.lookup(name)
	saved[name] = &savedVariable{
		value:        value,
		exists:       exists,
		unknown:      state.vars.isUnknown(name),
		indexedSlots: state.vars.indexedSlots(name),
	}
}

// Replace invokes a command and terminates each successful path as exec does.
func (c *CommandContext) executeReplace(name string, arguments []*Argument, clearEnvironment bool) ([]*pathResult, error) {
	state := c.state
	failure := state.snapshotForUnknownFailure()
	if clearEnvironment {
		state.vars.clearExported()
	}
	definition := c.execution.lookupCommandDefinition(name)
	unknownExternal := !commandDefinitionExecutable(definition) || definition.Fallback && !definition.Builtin
	paths, err := c.executeInvoke(name, arguments, false)
	if err != nil {
		return paths, err
	}
	for _, path := range paths {
		if path.status != StatusCompleted {
			continue
		}
		_, exitUnresolved := path.state.exitStatus.Data()
		if unknownExternal && exitUnresolved {
			path.state.exitFailure = failure
		}
		path.state.signal = signalExit
	}
	return paths, nil
}

// Evaluate parses and executes source in the current shell state.
func (c *CommandContext) executeEvaluate(source, name string, parseExitCode int) ([]*pathResult, error) {
	if status := c.execution.checkContext(c.state, c.source); status != StatusCompleted {
		return []*pathResult{{state: c.state, status: status}}, nil
	}
	return c.execution.evaluateSourceTextWithParseFailure(c.state, source, name, parseExitCode)
}

// Source evaluates source in source-file scope and restores positional
// arguments after every resulting path.
func (c *CommandContext) executeSource(source, name string, arguments []string) ([]*pathResult, error) {
	state := c.state
	if status := c.execution.checkContext(state, c.source); status != StatusCompleted {
		return []*pathResult{{state: state, status: status}}, nil
	}
	withArguments := len(arguments) != 0
	if withArguments {
		state.pushLocalScope()
		c.execution.setFunctionArguments(state, arguments)
	}
	state.sourceDepth++
	paths, err := c.execution.evaluateSourceTextWithParseFailure(state, source, name, 2)
	for _, path := range paths {
		if path.state.signal == signalReturn {
			path.state.signal = signalNone
		}
		path.state.sourceDepth--
		if withArguments {
			path.state.popLocalScope()
		}
	}
	return paths, err
}

// RunShell executes a parsed child-shell program in an isolated shell state.
func (c *CommandContext) executeRunShell(program *ShellProgram) ([]*pathResult, error) {
	parent := c.state
	releaseParent, err := c.execution.retainNestedShellParent(parent)
	if err != nil {
		return nil, err
	}
	defer releaseParent()
	child := newShellChild(parent)
	for name, enabled := range program.Options {
		if !setShellOption(child, name, enabled) {
			return nil, fmt.Errorf("unsupported child shell option %q", name)
		}
	}
	child.vars.put("0", expand.Variable{Set: true, Kind: expand.String, Str: program.Name})
	child.replacePositionalArguments(program.Arguments)
	if status := c.execution.checkContext(child, c.source); status != StatusCompleted {
		mergeIssue(parent, child)
		return []*pathResult{{state: parent, status: status}}, nil
	}
	if program.ParseOnly {
		if _, parseErr := Parse(c.execution.ctx, program.Source, program.Name); parseErr != nil {
			if result, resultErr, incomplete := c.execution.incompleteFromEvaluationError(child, parseErr, unknownLocation); incomplete {
				mergeIssue(parent, child)
				return []*pathResult{{state: parent, status: result.status}}, resultErr
			}
			child.setExitCode(2)
			status := c.execution.appendStreams(child, nil, []byte(parseErr.Error()+"\n"), false, false, c.source)
			return c.shellChildResults(parent, []*pathResult{{state: child, status: status}}, nil)
		}
		child.setExitCode(0)
		return c.shellChildResults(parent, []*pathResult{{state: child, status: StatusCompleted}}, nil)
	}
	childPaths, evaluationErr := c.execution.evaluateCommandSourceText(child, program.Source, program.Name, 2)
	childPaths, trapErr := c.execution.evaluateExitTraps(childPaths)
	if evaluationErr == nil {
		evaluationErr = trapErr
	}
	return c.shellChildResults(parent, childPaths, evaluationErr)
}

func (c *CommandContext) shellChildResults(parent *State, childPaths []*pathResult, evaluationErr error) ([]*pathResult, error) {
	paths := make([]*pathResult, 0, len(childPaths))
	for _, childPath := range childPaths {
		result := parent.clone()
		inheritPathIdentity(result, childPath.state)
		mergeIssue(result, childPath.state)
		mergeChildInput(result, childPath.state)
		result.fs = childPath.state.fs.clone()
		status := childPath.status
		stdout, stdoutUnresolved := childPath.state.stdout.Data()
		stderr, stderrUnresolved := childPath.state.stderr.Data()
		if outputStatus := c.execution.appendStreams(result, stdout, stderr, stdoutUnresolved, stderrUnresolved, c.source); outputStatus != StatusCompleted {
			status = outputStatus
		}
		exitCode, exitUnresolved := childPath.state.exitStatus.Data()
		result.setExitStatus(exitCode, exitUnresolved)
		paths = append(paths, &pathResult{state: result, status: status})
	}
	return paths, evaluationErr
}

func (e *ExecutionContext) retainNestedShellParent(parent *State) (func(), error) {
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	parentBytes, ok := stateMaterialization(parent)
	if ok {
		parentBytes, ok = materialize.Add(parentBytes, materialize.EntryBytes, maximum)
	}
	auxiliary := 0
	if ok {
		auxiliary, ok = e.retainedAuxiliaryBytes(maximum)
	}
	if ok {
		_, ok = materialize.Add(auxiliary, parentBytes, maximum)
	}
	nestedBytes := 0
	if ok {
		nestedBytes, ok = materialize.Add(e.nestedShellBytes, parentBytes, maximum)
	}
	if !ok {
		return nil, materialize.LimitError(maximum)
	}
	previous := e.nestedShellBytes
	e.nestedShellBytes = nestedBytes
	return func() {
		e.nestedShellBytes = previous
	}, nil
}
