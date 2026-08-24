package runtime

import (
	"maps"

	"mvdan.cc/sh/v3/syntax"
)

// CommandContext provides Shell execution capabilities for one command call.
// It and the State returned by State must not be retained after the call.
type CommandContext struct {
	execution    *ExecutionContext
	state        *State
	source       *location
	syntax       syntax.Command
	redirects    []*Redirect
	originalUser string
	userChanged  bool
}

// State returns the current execution-path state.
func (c *CommandContext) State() *State {
	return c.state
}

// ChangeUser changes the simulated user for the remainder of this command,
// including nested command operations. The runtime restores the previous user
// on every returned path when the command completes.
func (c *CommandContext) ChangeUser(user string) error {
	previous := c.state.user
	if !c.userChanged {
		c.originalUser = previous
	}
	c.state.user = user
	if err := c.state.checkPublicMutationMaterialization(); err != nil {
		c.state.user = previous
		return err
	}
	c.userChanged = true
	return nil
}

func (c *CommandContext) restoreUser(paths []*pathResult) {
	if !c.userChanged {
		return
	}
	states := make([]*State, 0, len(paths)+1)
	states = append(states, c.state)
	for _, path := range paths {
		states = append(states, path.state)
	}
	visited := make(map[*State]struct{})
	for len(states) != 0 {
		last := len(states) - 1
		state := states[last]
		states = states[:last]
		if state == nil {
			continue
		}
		if _, exists := visited[state]; exists {
			continue
		}
		visited[state] = struct{}{}
		state.user = c.originalUser
		states = append(states, state.exitFailure)
	}
}

// CommandSyntax returns the parser-special command node for a direct
// declaration or let clause. Ordinary calls and nested dispatches return nil.
func (c *CommandContext) CommandSyntax() syntax.Command {
	return c.syntax
}

// Redirects returns the expanded file and descriptor redirections active for
// this command. File targets are absolute paths in the virtual filesystem.
// The returned values must be treated as read-only and not retained.
func (c *CommandContext) Redirects() []*Redirect {
	return c.redirects
}

// ResultUnknown marks independently unknown output and exit dimensions.
func (c *CommandContext) ResultUnknown(result *CommandResult, stdout, stderr, exit bool) *CommandResult {
	if result == nil {
		result = &CommandResult{}
	}
	result.stdoutUnknown = stdout
	result.stderrUnknown = stderr
	result.exitUnknown = exit
	return result
}

// UnresolvedResult returns a command result whose output streams and exit
// status are all unresolved.
func (c *CommandContext) UnresolvedResult() *CommandResult {
	return NewUnresolvedResult()
}

// ResultCurrentExit appends output without replacing an exit status already
// established through State.
func (c *CommandContext) ResultCurrentExit(result *CommandResult, stdoutUnknown, stderrUnknown bool) *CommandResult {
	if result == nil {
		result = &CommandResult{}
	}
	result.stdoutUnknown = stdoutUnknown
	result.stderrUnknown = stderrUnknown
	result.preserveExit = true
	return result
}

// StopUnresolved stops the current path because required Shell semantics
// could not be determined.
func (c *CommandContext) StopUnresolved(reason string) *CommandResult {
	return operationResult(&commandOperation{kind: commandOperationUnresolved, message: reason})
}

// ExpansionError defers an expansion failure to the evaluator's standard
// incomplete or unresolved error handling.
func (c *CommandContext) ExpansionError(err error, message string) *CommandResult {
	return operationResult(&commandOperation{kind: commandOperationExpansionError, err: err, message: message})
}

// Invoke dispatches a command without consulting Shell functions.
func (c *CommandContext) Invoke(name string, arguments []*Argument, builtinOnly bool) *CommandResult {
	return operationResult(&commandOperation{
		kind:        commandOperationInvoke,
		name:        name,
		arguments:   cloneArguments(arguments),
		builtinOnly: builtinOnly,
	})
}

// InvokeWithEnvironment dispatches a non-builtin command with temporary
// exported-variable changes.
func (c *CommandContext) InvokeWithEnvironment(name string, arguments []*Argument, clear bool, unset []string, assignments map[string]string) *CommandResult {
	return operationResult(&commandOperation{
		kind:             commandOperationInvokeEnvironment,
		name:             name,
		arguments:        cloneArguments(arguments),
		clearEnvironment: clear,
		unset:            append([]string(nil), unset...),
		assignments:      maps.Clone(assignments),
	})
}

// Replace dispatches a command and terminates each successful path.
func (c *CommandContext) Replace(name string, arguments []*Argument, clearEnvironment bool) *CommandResult {
	return operationResult(&commandOperation{
		kind:             commandOperationReplace,
		name:             name,
		arguments:        cloneArguments(arguments),
		clearEnvironment: clearEnvironment,
	})
}

// Evaluate evaluates source in the current Shell.
func (c *CommandContext) Evaluate(source, name string, parseExitCode int) *CommandResult {
	return operationResult(&commandOperation{
		kind:          commandOperationEvaluate,
		source:        source,
		name:          name,
		parseExitCode: parseExitCode,
	})
}

// Source evaluates source in source-file scope and restores temporary
// positional arguments.
func (c *CommandContext) Source(source, name string, arguments []string) *CommandResult {
	return operationResult(&commandOperation{
		kind:            commandOperationSource,
		source:          source,
		name:            name,
		sourceArguments: append([]string(nil), arguments...),
	})
}

// RunShell evaluates a child Shell program in an isolated Shell state.
func (c *CommandContext) RunShell(program *ShellProgram) *CommandResult {
	copied := *program
	copied.Arguments = append([]string(nil), program.Arguments...)
	copied.Options = maps.Clone(program.Options)
	return operationResult(&commandOperation{kind: commandOperationRunShell, program: &copied})
}

func operationResult(operation *commandOperation) *CommandResult {
	return &CommandResult{operation: operation}
}

func cloneArguments(arguments []*Argument) []*Argument {
	cloned := make([]*Argument, len(arguments))
	for index, argument := range arguments {
		copied := *argument
		cloned[index] = &copied
	}
	return cloned
}

func cloneRedirects(redirects []*Redirect) []*Redirect {
	cloned := make([]*Redirect, len(redirects))
	for index, redirect := range redirects {
		copied := *redirect
		cloned[index] = &copied
	}
	return cloned
}
