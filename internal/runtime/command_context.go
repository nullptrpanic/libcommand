package runtime

import (
	"maps"

	"mvdan.cc/sh/v3/syntax"
)

// CommandContext provides Shell execution capabilities for one command call.
// It and the State returned by State must not be retained after the call.
type CommandContext struct {
	execution *ExecutionContext
	state     *State
	source    *location
	syntax    syntax.Command
}

// State returns the current execution-path state.
func (c *CommandContext) State() *State {
	return c.state
}

// CommandSyntax returns the parser-special command node for a direct
// declaration or let clause. Ordinary calls and nested dispatches return nil.
func (c *CommandContext) CommandSyntax() syntax.Command {
	return c.syntax
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
