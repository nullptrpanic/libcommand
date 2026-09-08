package runtime

import (
	"bytes"
	"context"
)

// Request contains the initial state for one internal evaluation.
type Request struct {
	Source string
	Env    map[string]string
	Args   []string
	// Stdin initializes the concrete input stream; nil represents EOF.
	Stdin      []byte
	Files      map[string][]byte
	WorkingDir string
	User       string
}

// ArgumentKind describes whether one expanded command argument is concrete.
type ArgumentKind uint8

const (
	// ArgumentString is a fully resolved string argument.
	ArgumentString ArgumentKind = iota
	// ArgumentUnresolved represents an argument whose value cannot be resolved.
	ArgumentUnresolved
)

// Argument is one expanded command argument passed to a registered command.
// Value is empty when Kind is ArgumentUnresolved.
type Argument struct {
	Kind  ArgumentKind `json:"kind"`
	Value string       `json:"value"`
}

// InvocationUnresolved identifies non-argument invocation fields whose values
// could not be resolved by the simulator.
type InvocationUnresolved struct {
	// Name reports that Invocation.Name could not be resolved.
	Name bool `json:"name,omitempty"`
	// Env lists unresolved exported variable names in sorted order.
	Env []string `json:"env,omitempty"`
	// Dir reports that Invocation.Dir is unresolved.
	Dir bool `json:"dir,omitempty"`
	// Stdin reports that Invocation.Stdin is unresolved.
	Stdin bool `json:"stdin,omitempty"`
}

// Invocation is the expanded command passed to a registered command.
// Commands must treat the value and all referenced fields as read-only.
type Invocation struct {
	Name       string                `json:"name"`
	Args       []*Argument           `json:"args"`
	Env        map[string]string     `json:"env"`
	Dir        string                `json:"dir"`
	Stdin      []byte                `json:"stdin"`
	Unresolved *InvocationUnresolved `json:"unresolved,omitempty"`
}

// Redirect is one expanded file or descriptor redirection attached to the
// current Shell statement. Here-document and here-string contents are exposed
// through Invocation.Stdin instead.
type Redirect struct {
	FD         int    `json:"fd"`
	Operator   string `json:"operator"`
	Target     string `json:"target"`
	Unresolved bool   `json:"unresolved,omitempty"`
}

// CommandAction controls evaluation after a command returns.
type CommandAction uint8

const (
	// CommandContinue resumes Bash evaluation and is the zero-value action.
	CommandContinue CommandAction = iota
	// CommandStop terminates every active and pending execution path.
	CommandStop
)

// CommandResult is the simulated process result returned by a command.
// Returning it transfers ownership of every output to the runtime.
type CommandResult struct {
	Action    CommandAction
	operation *commandOperation
	outputs   []*CommandOutput
}

// CommandOutput is one possible process output returned by a command. Stdout,
// Stderr, and ExitCode must not be mutated after the command returns.
type CommandOutput struct {
	Stdout   *Uncertain[[]byte]
	Stderr   *Uncertain[[]byte]
	ExitCode *Uncertain[int]
	state    *State
}

// CommandOutputBuilder constructs one CommandOutput. Its zero output is
// resolved empty stdout, resolved empty stderr, and resolved exit code zero.
type CommandOutputBuilder struct {
	output CommandOutput
}

// AllUnresolved reports whether stdout, stderr, and exit status are all
// unresolved. Partially unresolved results return false.
func (r *CommandResult) AllUnresolved() bool {
	_, _, _, stdoutUnresolved, stderrUnresolved, exitUnresolved := commandResultValues(r)
	return stdoutUnresolved && stderrUnresolved && exitUnresolved
}

func commandResultValues(result *CommandResult) (stdout, stderr []byte, exitCode int, stdoutUnresolved, stderrUnresolved, exitCodeUnresolved bool) {
	if result == nil || len(result.outputs) == 0 {
		return nil, nil, 0, false, false, false
	}
	stdout, stderr, exitCode, stdoutUnresolved, stderrUnresolved, exitCodeUnresolved = result.outputs[0].values()
	for _, output := range result.outputs[1:] {
		candidateStdout, candidateStderr, candidateExitCode, candidateStdoutUnresolved, candidateStderrUnresolved, candidateExitCodeUnresolved := output.values()
		stdoutUnresolved = stdoutUnresolved || candidateStdoutUnresolved || !bytes.Equal(stdout, candidateStdout)
		stderrUnresolved = stderrUnresolved || candidateStderrUnresolved || !bytes.Equal(stderr, candidateStderr)
		exitCodeUnresolved = exitCodeUnresolved || candidateExitCodeUnresolved || exitCode != candidateExitCode
	}
	return stdout, stderr, exitCode, stdoutUnresolved, stderrUnresolved, exitCodeUnresolved
}

func (o *CommandOutput) values() (stdout, stderr []byte, exitCode int, stdoutUnresolved, stderrUnresolved, exitCodeUnresolved bool) {
	stdout, stdoutUnresolved = o.Stdout.Data()
	stderr, stderrUnresolved = o.Stderr.Data()
	exitCode, exitCodeUnresolved = o.ExitCode.Data()
	return stdout, stderr, exitCode, stdoutUnresolved, stderrUnresolved, exitCodeUnresolved
}

// Outputs returns the outputs explicitly added to this result. The slice is a
// copy; its output values remain owned by the result and must not be mutated
// after the command returns.
func (r *CommandResult) Outputs() []*CommandOutput {
	return append([]*CommandOutput(nil), r.outputs...)
}

// Command executes one command through the active simulation. Runtime path
// construction remains private to CommandContext and the evaluator.
type Command func(context.Context, *CommandContext, *Invocation) (*CommandResult, error)

type commandOperationKind uint8

const (
	commandOperationUnresolved commandOperationKind = iota
	commandOperationExpansionError
	commandOperationInvoke
	commandOperationInvokeEnvironment
	commandOperationReplace
	commandOperationEvaluate
	commandOperationSource
	commandOperationRunShell
)

type commandOperation struct {
	kind             commandOperationKind
	name             string
	argv0            *string
	source           string
	message          string
	err              error
	parseExitCode    int
	arguments        []*Argument
	builtinOnly      bool
	clearEnvironment bool
	unset            []string
	assignments      map[string]string
	program          *ShellProgram
}

// CommandDefinition describes one active command implementation. Fallback
// definitions participate only after an exact executable lookup misses.
type CommandDefinition struct {
	Command            Command
	Prepare            func(*CommandContext, *Invocation) error
	Observed           bool
	Builtin            bool
	Candidate          bool
	RestoreAssignments bool
	UserOverride       bool
	Fallback           bool
}

// CommandLookupFunc returns the selected exact or fallback definition. A
// fallback definition must set Fallback so Shell builtins and lookup queries
// retain their exact semantics.
type CommandLookupFunc func(string) *CommandDefinition

// Config controls internal evaluation dependencies.
type Config struct {
	LookupCommand     CommandLookupFunc
	MaxExecutionSteps int
	MaxMemoryBytes    int
	Trace             TraceObserver
	TraceOptions      *TraceOptions
}

// Status describes one internal execution-path terminal state.
type Status uint8

const (
	StatusCompleted Status = iota
	StatusTerminated
	StatusIncomplete
	StatusUnresolved
)
