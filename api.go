package libcommand

import (
	shellruntime "github.com/nullptrpanic/libcommand/internal/runtime"
)

// Simulator parses and evaluates Bash source without executing host commands.
// Commands without an exact registration use the configured "*" fallback. Its
// default result has no side effects and an unknown exit status, so
// status-dependent control flow explores both outcomes.
// A Simulator must be constructed with Builder. It is immutable after
// construction and may be reused concurrently.
type Simulator struct {
	commands map[string]*shellruntime.CommandDefinition
	limits   *Limits
}

// SimulationRequest describes one Bash program and its initial process state.
type SimulationRequest struct {
	Source string
	Env    map[string]string
	// Args initializes the script positional parameters $1, $2, ... and $@.
	// The script name exposed as $0 is "command.sh".
	Args []string
	// Stdin initializes the finite standard-input stream. Nil and an empty
	// slice both represent immediate EOF.
	Stdin []byte
	// Files initializes regular files in the isolated virtual filesystem.
	// Relative paths are resolved against WorkingDir, and required parent
	// directories are created. During redirection evaluation, reading a missing
	// file materializes an empty file, and writing one creates its missing
	// parent directories. The simulator copies the map and its contents before
	// evaluation.
	Files map[string][]byte
	// WorkingDir initializes PWD and the current directory. An empty value
	// preserves the default root directory "/". Relative paths are resolved
	// from that virtual root.
	WorkingDir string
	// User initializes the simulated current user. An empty value defaults to
	// "user". It does not implicitly alter USER, LOGNAME, HOME, UID, or EUID.
	User string
}

// Invocation is the expanded command passed to a registered command.
type Invocation = shellruntime.Invocation

// ArgumentKind describes whether one expanded command argument is concrete.
type ArgumentKind = shellruntime.ArgumentKind

const (
	// ArgumentString is a fully resolved string argument.
	ArgumentString = shellruntime.ArgumentString
	// ArgumentUnresolved represents an argument whose value cannot be resolved.
	ArgumentUnresolved = shellruntime.ArgumentUnresolved
)

// Argument is one expanded command argument passed to a registered command.
type Argument = shellruntime.Argument

// InvocationUnresolved identifies invocation fields whose values could not be
// resolved by the simulator.
type InvocationUnresolved = shellruntime.InvocationUnresolved

// Redirect is one expanded file or descriptor redirection active for a
// registered command.
type Redirect = shellruntime.Redirect

// CommandAction controls evaluation after a command returns.
type CommandAction = shellruntime.CommandAction

const (
	// CommandContinue resumes Bash evaluation and is the zero-value action.
	CommandContinue = shellruntime.CommandContinue
	// CommandStop terminates every active and pending execution path.
	CommandStop = shellruntime.CommandStop
)

// CommandResult is the simulated process result returned by a command. Set
// Unresolved when the command cannot determine its output or exit status. The
// runtime trusts commands to satisfy the result contract.
type CommandResult = shellruntime.CommandResult

// State is the current simulated shell state. Commands may inspect and mutate
// it through its methods. The pointer is valid only for the active call.
type State = shellruntime.State

// CommandContext provides Shell execution capabilities for one command call.
// It and the State returned by State must not be retained after the call.
type CommandContext = shellruntime.CommandContext

// Command handles one expanded invocation. Commands can be called concurrently
// by separate Simulate calls and must be concurrency-safe. A nil result and nil
// error use unresolved-command behavior. A non-nil error aborts simulation.
// Panics are converted to simulation errors.
type Command = shellruntime.Command

// CommandMiddleware decorates one command. Middleware registered first wraps
// middleware registered later and therefore observes the call first and the
// result last. A middleware may short-circuit without calling next, but must
// not call next more than once or retain call-scoped arguments after returning.
type CommandMiddleware func(Command) Command

// ArithmeticResult is one arithmetic evaluation requested by a command.
type ArithmeticResult = shellruntime.ArithmeticResult

// PathKind describes one virtual filesystem path.
type PathKind = shellruntime.PathKind

const (
	PathMissing   = shellruntime.PathMissing
	PathFile      = shellruntime.PathFile
	PathDirectory = shellruntime.PathDirectory
	PathDevice    = shellruntime.PathDevice
)

// CommandKind describes one definition selected by Shell lookup.
type CommandKind = shellruntime.CommandKind

const (
	CommandMissing  = shellruntime.CommandMissing
	CommandFunction = shellruntime.CommandFunction
	CommandBuiltin  = shellruntime.CommandBuiltin
	CommandFile     = shellruntime.CommandFile
)

// ShellProgram describes one child Shell execution.
type ShellProgram = shellruntime.ShellProgram

// TraceEventKind identifies one immutable simulation trace event.
type TraceEventKind = shellruntime.TraceEventKind

const (
	TraceSimulationStarted  = shellruntime.TraceSimulationStarted
	TraceNodeDiscovered     = shellruntime.TraceNodeDiscovered
	TraceStatementStarted   = shellruntime.TraceStatementStarted
	TraceStatementActivated = shellruntime.TraceStatementActivated
	TraceStatementFinished  = shellruntime.TraceStatementFinished
	TracePathForked         = shellruntime.TracePathForked
	TraceCommandStarted     = shellruntime.TraceCommandStarted
	TraceCommandFinished    = shellruntime.TraceCommandFinished
	TracePathCompleted      = shellruntime.TracePathCompleted
	TraceSimulationFinished = shellruntime.TraceSimulationFinished
)

// TraceSource identifies a range in one parsed Shell source.
type TraceSource = shellruntime.TraceSource

// TraceNode is one statement in the static syntax skeleton.
type TraceNode = shellruntime.TraceNode

// TraceMemory is a logical retained-size snapshot.
type TraceMemory = shellruntime.TraceMemory

// TraceCommandResult describes a traced command result. Direct stream contents
// are present only when state snapshots are enabled.
type TraceCommandResult = shellruntime.TraceCommandResult

// TracePathResult is the final observable state of one retained path.
type TracePathResult = shellruntime.TracePathResult

// TraceOptions controls optional trace-only data.
type TraceOptions = shellruntime.TraceOptions

// TraceVariable is one Shell variable captured in a trace state snapshot.
type TraceVariable = shellruntime.TraceVariable

// TraceStateSnapshot is the Shell context before or after one statement.
type TraceStateSnapshot = shellruntime.TraceStateSnapshot

// TraceEvent is one item in a simulation trace stream.
type TraceEvent = shellruntime.TraceEvent

// TracePathStatus describes a traced execution path's state.
type TracePathStatus = shellruntime.Status

const (
	// TraceStatusCompleted indicates a path completed normally.
	TraceStatusCompleted = shellruntime.StatusCompleted
	// TraceStatusTerminated indicates a path ended through Shell control flow.
	TraceStatusTerminated = shellruntime.StatusTerminated
	// TraceStatusIncomplete indicates evaluation stopped with an issue.
	TraceStatusIncomplete = shellruntime.StatusIncomplete
	// TraceStatusUnresolved indicates a retained path with unresolved semantics.
	TraceStatusUnresolved = shellruntime.StatusUnresolved
)

// TraceObserver receives trace events synchronously. Returning false disables
// further tracing without changing the simulation.
type TraceObserver = shellruntime.TraceObserver
