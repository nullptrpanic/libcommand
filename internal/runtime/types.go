package runtime

import (
	"context"
)

// Request contains the initial state for one internal evaluation.
type Request struct {
	Source string
	Env    map[string]string
	Args   []string
	// Stdin initializes the concrete input stream; nil represents EOF.
	Stdin []byte
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

// CommandAction controls evaluation after a command returns.
type CommandAction uint8

const (
	// CommandContinue resumes Bash evaluation and is the zero-value action.
	CommandContinue CommandAction = iota
	// CommandStop terminates every active and pending execution path.
	CommandStop
)

// CommandResult is the simulated process result returned by a command.
// Returning it transfers ownership of Stdout and Stderr to the runtime; the
// command must not mutate either slice after returning.
type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Action   CommandAction
	// Unresolved reports that stdout, stderr, and exit status cannot be
	// determined. The other public fields must retain their zero values.
	Unresolved    bool
	operation     *commandOperation
	stdoutUnknown bool
	stderrUnknown bool
	exitUnknown   bool
	preserveExit  bool
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
	source           string
	message          string
	err              error
	parseExitCode    int
	arguments        []*Argument
	sourceArguments  []string
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
