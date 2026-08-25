package runtime

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// TraceEventKind identifies one immutable simulation trace event.
type TraceEventKind string

const (
	TraceSimulationStarted  TraceEventKind = "simulation_started"
	TraceNodeDiscovered     TraceEventKind = "node_discovered"
	TraceStatementStarted   TraceEventKind = "statement_started"
	TraceStatementActivated TraceEventKind = "statement_activated"
	TraceStatementFinished  TraceEventKind = "statement_finished"
	TracePathForked         TraceEventKind = "path_forked"
	TraceCommandStarted     TraceEventKind = "command_started"
	TraceCommandFinished    TraceEventKind = "command_finished"
	TracePathCompleted      TraceEventKind = "path_completed"
	TraceSimulationFinished TraceEventKind = "simulation_finished"
)

// TraceSource identifies a range in one parsed Shell source.
type TraceSource struct {
	Name      string `json:"name"`
	Line      uint   `json:"line"`
	Column    uint   `json:"column"`
	EndLine   uint   `json:"endLine"`
	EndColumn uint   `json:"endColumn"`
}

// TraceNode is one statement in the static syntax skeleton. Nodes that never
// receive an execution event remain useful to clients as unexecuted syntax.
type TraceNode struct {
	ID       uint64       `json:"id"`
	ParentID uint64       `json:"parentId,omitempty"`
	Kind     string       `json:"kind"`
	Snippet  string       `json:"snippet"`
	Source   *TraceSource `json:"source"`
	// Embedded marks a nested evaluation detail that presentation clients may
	// fold into its parent. The node and all of its execution events remain in
	// the trace for runtime correlation.
	Embedded bool `json:"embedded,omitempty"`
	// FlowGroup identifies alternative statement bodies under the same parent.
	// Statements in one group are sequential; distinct groups may be laid out
	// alongside one another. It does not affect evaluation.
	FlowGroup uint32 `json:"flowGroup,omitempty"`
	// FlowCanSkip marks a statement whose control flow can reach the following
	// statement without entering any visible child group, such as an if without
	// an else, a case with no matching pattern, or a loop with zero iterations.
	// It does not affect evaluation.
	FlowCanSkip bool `json:"flowCanSkip,omitempty"`
	// FlowCommand is the literal command name of a simple command. Presentation
	// clients use it to distinguish Shell control transfers and direct function
	// calls without attempting to parse the display snippet.
	FlowCommand string `json:"flowCommand,omitempty"`
	// FlowFunction is the declared function name. A function body is syntax
	// owned by its declaration, but is only entered when this name is called.
	FlowFunction string `json:"flowFunction,omitempty"`
	// FlowGroupExit preserves the operator terminating a case item. It is copied
	// to each visible statement in that item so clients can reconstruct ;& and
	// ;;& flow after embedded statements have been folded.
	FlowGroupExit string `json:"flowGroupExit,omitempty"`
	// FlowGroupDefault marks a case item containing an unquoted literal *.
	FlowGroupDefault bool `json:"flowGroupDefault,omitempty"`
}

// TraceMemory is a logical retained-size snapshot. It describes the same
// package-owned data measured by MaxMemoryBytes, not the Go or WASM heap.
type TraceMemory struct {
	StateBytes        int `json:"stateBytes"`
	AggregateBytes    int `json:"aggregateBytes"`
	VariablesBytes    int `json:"variablesBytes"`
	VirtualFileBytes  int `json:"virtualFileBytes"`
	StreamBytes       int `json:"streamBytes"`
	FunctionBytes     int `json:"functionBytes"`
	SubstitutionBytes int `json:"substitutionBytes"`
	OtherBytes        int `json:"otherBytes"`
	AuxiliaryBytes    int `json:"auxiliaryBytes"`
	RetainedPaths     int `json:"retainedPaths"`
	MaximumBytes      int `json:"maximumBytes"`
}

// TraceCommandResult describes one command call's direct result. Stdout and
// Stderr are copied only when state snapshots are enabled; byte counts remain
// available to lightweight observers.
type TraceCommandResult struct {
	Stdout             string        `json:"stdout,omitempty"`
	Stderr             string        `json:"stderr,omitempty"`
	ExitCode           int           `json:"exitCode"`
	StdoutBytes        int           `json:"stdoutBytes"`
	StderrBytes        int           `json:"stderrBytes"`
	Action             CommandAction `json:"action"`
	StdoutUnresolved   bool          `json:"stdoutUnresolved"`
	StderrUnresolved   bool          `json:"stderrUnresolved"`
	ExitCodeUnresolved bool          `json:"exitCodeUnresolved"`
	Unresolved         bool          `json:"unresolved"`
	OutputCaptured     bool          `json:"outputCaptured"`
	OutputTruncated    bool          `json:"outputTruncated,omitempty"`
}

// TracePathResult is the final observable state of one retained execution
// path. Unresolved fields retain their representative value only for debugging;
// consumers must inspect the corresponding unresolved flag.
type TracePathResult struct {
	Stdout             string `json:"stdout"`
	Stderr             string `json:"stderr"`
	ExitCode           int    `json:"exitCode"`
	StdoutUnresolved   bool   `json:"stdoutUnresolved"`
	StderrUnresolved   bool   `json:"stderrUnresolved"`
	ExitCodeUnresolved bool   `json:"exitCodeUnresolved"`
	Error              string `json:"error,omitempty"`
}

// TraceOptions controls optional trace-only data. State snapshots are disabled
// by default because they copy state for display and are not needed to execute
// the simulation.
type TraceOptions struct {
	// StateSnapshots captures full Shell context on statement events and direct
	// command output on command-finished events.
	StateSnapshots bool
	// MaxSnapshotBytes bounds all snapshots and copied command output produced
	// by one simulation.
	MaxSnapshotBytes int
}

// TraceVariable is one Shell variable captured in a state snapshot. Value is
// a representative scalar value or a stable index=value listing for arrays.
type TraceVariable struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Value      string `json:"value"`
	Exported   bool   `json:"exported"`
	ReadOnly   bool   `json:"readOnly"`
	Unresolved bool   `json:"unresolved"`
}

// TraceStateSnapshot is the observable Shell context immediately before or
// after one statement. Unresolved flags qualify their representative values.
type TraceStateSnapshot struct {
	Directory           string           `json:"directory"`
	DirectoryUnresolved bool             `json:"directoryUnresolved"`
	Args                []string         `json:"args"`
	ArgsUnresolved      bool             `json:"argsUnresolved"`
	Stdin               string           `json:"stdin"`
	StdinUnresolved     bool             `json:"stdinUnresolved"`
	Stdout              string           `json:"stdout"`
	StdoutUnresolved    bool             `json:"stdoutUnresolved"`
	Stderr              string           `json:"stderr"`
	StderrUnresolved    bool             `json:"stderrUnresolved"`
	ExitCode            int              `json:"exitCode"`
	ExitCodeUnresolved  bool             `json:"exitCodeUnresolved"`
	Error               string           `json:"error,omitempty"`
	Variables           []*TraceVariable `json:"variables"`
}

// TraceEvent is one item in a simulation trace stream.
type TraceEvent struct {
	Sequence          uint64              `json:"sequence"`
	Kind              TraceEventKind      `json:"kind"`
	Node              *TraceNode          `json:"node,omitempty"`
	NodeID            uint64              `json:"nodeId,omitempty"`
	PathID            uint64              `json:"pathId,omitempty"`
	ParentPathID      uint64              `json:"parentPathId,omitempty"`
	ChildPathIDs      []uint64            `json:"childPathIds,omitempty"`
	PreviousSequence  uint64              `json:"previousSequence,omitempty"`
	Steps             int                 `json:"steps,omitempty"`
	Memory            *TraceMemory        `json:"memory,omitempty"`
	Invocation        *Invocation         `json:"invocation,omitempty"`
	CommandResult     *TraceCommandResult `json:"commandResult,omitempty"`
	PathResult        *TracePathResult    `json:"pathResult,omitempty"`
	Snapshot          *TraceStateSnapshot `json:"snapshot,omitempty"`
	SnapshotTruncated bool                `json:"snapshotTruncated,omitempty"`
	Status            Status              `json:"status"`
	Error             string              `json:"error,omitempty"`
}

// TraceObserver receives trace events synchronously. Returning false disables
// further tracing without stopping or otherwise changing the simulation.
type TraceObserver func(*TraceEvent) bool

type executionTrace struct {
	observer         TraceObserver
	sequence         uint64
	nextNodeID       uint64
	nodeIDs          map[syntax.Node]uint64
	nodes            map[uint64]*TraceNode
	lastNode         map[uint64]uint64
	lastEvent        map[uint64]uint64
	aggregate        int
	paths            int
	options          *TraceOptions
	snapshotBytes    int
	snapshotsStopped bool
}

func newExecutionTrace(observer TraceObserver, options *TraceOptions) *executionTrace {
	if observer == nil {
		return nil
	}
	var copiedOptions *TraceOptions
	if options != nil {
		value := *options
		copiedOptions = &value
	}
	return &executionTrace{
		observer:  observer,
		options:   copiedOptions,
		nodeIDs:   make(map[syntax.Node]uint64),
		nodes:     make(map[uint64]*TraceNode),
		lastNode:  make(map[uint64]uint64),
		lastEvent: make(map[uint64]uint64),
	}
}

func (trace *executionTrace) emit(event *TraceEvent) uint64 {
	if trace == nil || trace.observer == nil {
		return 0
	}
	trace.sequence++
	event.Sequence = trace.sequence
	if !trace.observer(event) {
		trace.observer = nil
	}
	return event.Sequence
}

func (trace *executionTrace) start() {
	trace.emit(&TraceEvent{Kind: TraceSimulationStarted})
}

func (trace *executionTrace) finish(err error) {
	event := &TraceEvent{Kind: TraceSimulationFinished}
	if err != nil {
		event.Error = err.Error()
	}
	trace.emit(event)
}

func (trace *executionTrace) discover(file *syntax.File, source, name string, parentID uint64) {
	if trace == nil || trace.observer == nil || file == nil {
		return
	}
	for _, statement := range file.Stmts {
		trace.discoverStatement(statement, source, name, parentID, false, 0, "", false)
		if trace.observer == nil {
			return
		}
	}
}

func (trace *executionTrace) discoverStatement(statement *syntax.Stmt, source, name string, parentID uint64, embedded bool, flowGroup uint32, flowGroupExit string, flowGroupDefault bool) {
	if statement == nil || trace.nodeIDs[statement] != 0 {
		return
	}
	trace.nextNodeID++
	node := &TraceNode{
		ID:               trace.nextNodeID,
		ParentID:         parentID,
		Kind:             traceStatementKind(statement),
		Snippet:          traceSnippet(source, statement),
		Source:           traceSource(name, statement),
		Embedded:         embedded,
		FlowGroup:        flowGroup,
		FlowCanSkip:      traceFlowCanSkip(statement),
		FlowCommand:      traceFlowCommand(statement),
		FlowFunction:     traceFlowFunction(statement),
		FlowGroupExit:    flowGroupExit,
		FlowGroupDefault: flowGroupDefault,
	}
	trace.nodeIDs[statement] = node.ID
	trace.nodes[node.ID] = node
	trace.emit(&TraceEvent{Kind: TraceNodeDiscovered, Node: node})
	if trace.observer == nil {
		return
	}

	visibleChildren := traceVisibleChildGroups(statement)
	syntax.Walk(statement, func(child syntax.Node) bool {
		if child == nil {
			return trace.observer != nil
		}
		if child == statement {
			return true
		}
		if childStatement, ok := child.(*syntax.Stmt); ok {
			flow, visible := visibleChildren[childStatement]
			if flow == nil {
				flow = &traceChildFlow{}
			}
			trace.discoverStatement(childStatement, source, name, node.ID, !visible, flow.group, flow.groupExit, flow.groupDefault)
			return false
		}
		trace.nodeIDs[child] = node.ID
		return trace.observer != nil
	})
}

// traceVisibleChildGroups returns statement bodies that remain useful in
// the static syntax view. Other nested statements are implementation details
// of evaluating their parent, such as conditions, substitutions, and pipeline
// operands. They stay in the trace for runtime correlation but are marked as
// embedded so presentation clients may fold them.
type traceChildFlow struct {
	group        uint32
	groupExit    string
	groupDefault bool
}

func traceVisibleChildGroups(statement *syntax.Stmt) map[*syntax.Stmt]*traceChildFlow {
	children := make(map[*syntax.Stmt]*traceChildFlow)
	add := func(group uint32, groupExit string, groupDefault bool, statements ...*syntax.Stmt) {
		for _, child := range statements {
			if child != nil {
				children[child] = &traceChildFlow{group: group, groupExit: groupExit, groupDefault: groupDefault}
			}
		}
	}

	switch command := statement.Cmd.(type) {
	case *syntax.IfClause:
		var group uint32
		for clause := command; clause != nil; clause = clause.Else {
			add(group, "", false, clause.Then...)
			group++
		}
	case *syntax.WhileClause:
		add(0, "", false, command.Do...)
	case *syntax.ForClause:
		add(0, "", false, command.Do...)
	case *syntax.CaseClause:
		for index, item := range command.Items {
			add(uint32(index), item.Op.String(), traceCaseItemDefault(item), item.Stmts...)
		}
	case *syntax.Subshell:
		add(0, "", false, command.Stmts...)
	case *syntax.Block:
		add(0, "", false, command.Stmts...)
	case *syntax.FuncDecl:
		add(0, "", false, command.Body)
	case *syntax.TimeClause:
		add(0, "", false, command.Stmt)
	case *syntax.CoprocClause:
		add(0, "", false, command.Stmt)
	}
	return children
}

func traceFlowCanSkip(statement *syntax.Stmt) bool {
	switch command := statement.Cmd.(type) {
	case *syntax.IfClause:
		last := command
		for last.Else != nil {
			last = last.Else
		}
		return last.ThenPos.IsValid()
	case *syntax.CaseClause:
		for _, item := range command.Items {
			if traceCaseItemDefault(item) {
				return false
			}
		}
		return true
	case *syntax.ForClause:
		loop, arithmetic := command.Loop.(*syntax.CStyleLoop)
		return !arithmetic || loop.Cond != nil
	case *syntax.WhileClause:
		return true
	default:
		return false
	}
}

func traceCaseItemDefault(item *syntax.CaseItem) bool {
	if item == nil {
		return false
	}
	for _, pattern := range item.Patterns {
		if len(pattern.Parts) != 1 {
			continue
		}
		literal, ok := pattern.Parts[0].(*syntax.Lit)
		if ok && literal.Value == "*" {
			return true
		}
	}
	return false
}

func traceFlowCommand(statement *syntax.Stmt) string {
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return ""
	}
	return call.Args[0].Lit()
}

func traceFlowFunction(statement *syntax.Stmt) string {
	declaration, ok := statement.Cmd.(*syntax.FuncDecl)
	if !ok || declaration.Name == nil {
		return ""
	}
	return declaration.Name.Value
}

func (trace *executionTrace) currentNodeID(state *State) uint64 {
	if trace == nil || state == nil {
		return 0
	}
	pathID, _ := tracePathIDs(state)
	return trace.lastNode[pathID]
}

func tracePathIDs(state *State) (uint64, uint64) {
	if state == nil {
		return 0, 0
	}
	parentPathID := uint64(0)
	if state.parent != nil {
		parentPathID = state.parent.pathID
	}
	return state.pathID, parentPathID
}

func (trace *executionTrace) nodeID(node syntax.Node) uint64 {
	if trace == nil || node == nil {
		return 0
	}
	return trace.nodeIDs[node]
}

func (trace *executionTrace) statementStarted(execution *ExecutionContext, state *State, statement *syntax.Stmt, budget *retainedPathBudget) {
	if trace == nil || trace.observer == nil {
		return
	}
	pathID, parentPathID := tracePathIDs(state)
	nodeID := trace.nodeIDs[statement]
	if nodeID == 0 {
		return
	}
	memory := execution.traceMemory(state, budget)
	trace.aggregate = memory.AggregateBytes
	trace.paths = memory.RetainedPaths
	snapshot, snapshotTruncated := trace.stateSnapshot(state)
	sequence := trace.emit(&TraceEvent{
		Kind:              TraceStatementStarted,
		Node:              trace.nodes[nodeID],
		NodeID:            nodeID,
		PathID:            pathID,
		ParentPathID:      parentPathID,
		PreviousSequence:  trace.lastEvent[pathID],
		Steps:             execution.executedSteps,
		Memory:            memory,
		Snapshot:          snapshot,
		SnapshotTruncated: snapshotTruncated,
	})
	trace.lastNode[pathID] = nodeID
	trace.lastEvent[pathID] = sequence
}

// statementActivated reports that control re-entered a statement which still
// has one active evaluation lifetime. Loops use it for iterations after the
// first so trace consumers can replay every control-flow visit without
// manufacturing nested statement-start/finish pairs.
func (trace *executionTrace) statementActivated(execution *ExecutionContext, state *State, node syntax.Node) {
	if trace == nil || trace.observer == nil || state == nil || node == nil {
		return
	}
	pathID, parentPathID := tracePathIDs(state)
	nodeID := trace.nodeIDs[node]
	if nodeID == 0 {
		return
	}
	snapshot, snapshotTruncated := trace.stateSnapshot(state)
	sequence := trace.emit(&TraceEvent{
		Kind:              TraceStatementActivated,
		Node:              trace.nodes[nodeID],
		NodeID:            nodeID,
		PathID:            pathID,
		ParentPathID:      parentPathID,
		PreviousSequence:  trace.lastEvent[pathID],
		Steps:             execution.executedSteps,
		Memory:            execution.traceMemoryWithAggregate(state, trace.aggregate, trace.paths),
		Snapshot:          snapshot,
		SnapshotTruncated: snapshotTruncated,
	})
	trace.lastNode[pathID] = nodeID
	trace.lastEvent[pathID] = sequence
}

func (trace *executionTrace) pathForked(parent *State, nodeID uint64, children []*State) {
	if trace == nil || trace.observer == nil || len(children) == 0 {
		return
	}
	childIDs := make([]uint64, len(children))
	for index, child := range children {
		childID := child.pathID
		childIDs[index] = childID
		trace.lastNode[childID] = nodeID
	}
	parentPathID := uint64(0)
	if parent.parent != nil {
		parentPathID = parent.parent.pathID
	}
	sequence := trace.emit(&TraceEvent{
		Kind:             TracePathForked,
		Node:             trace.nodes[nodeID],
		NodeID:           nodeID,
		PathID:           parent.pathID,
		ParentPathID:     parentPathID,
		ChildPathIDs:     childIDs,
		PreviousSequence: trace.lastEvent[parent.pathID],
	})
	trace.lastEvent[parent.pathID] = sequence
	for _, childID := range childIDs {
		trace.lastEvent[childID] = sequence
	}
}

func (trace *executionTrace) statementFinished(execution *ExecutionContext, statement *syntax.Stmt, successors []*pathResult, budget *retainedPathBudget) {
	if trace == nil || trace.observer == nil {
		return
	}
	nodeID := trace.nodeIDs[statement]
	for _, successor := range successors {
		memory := execution.traceMemory(successor.state, budget)
		trace.aggregate = memory.AggregateBytes
		trace.paths = memory.RetainedPaths
		pathID, parentPathID := tracePathIDs(successor.state)
		snapshot, snapshotTruncated := trace.stateSnapshot(successor.state)
		sequence := trace.emit(&TraceEvent{
			Kind:              TraceStatementFinished,
			Node:              trace.nodes[nodeID],
			NodeID:            nodeID,
			PathID:            pathID,
			ParentPathID:      parentPathID,
			PreviousSequence:  trace.lastEvent[pathID],
			Steps:             execution.executedSteps,
			Memory:            memory,
			Snapshot:          snapshot,
			SnapshotTruncated: snapshotTruncated,
			Status:            successor.status,
		})
		trace.lastEvent[pathID] = sequence
	}
}

func (trace *executionTrace) stateSnapshot(state *State) (*TraceStateSnapshot, bool) {
	if trace == nil || trace.options == nil || !trace.options.StateSnapshots || state == nil {
		return nil, false
	}
	if trace.snapshotsStopped {
		return nil, true
	}
	remaining := trace.options.MaxSnapshotBytes - trace.snapshotBytes
	size, fits := traceStateSnapshotBytes(state, remaining)
	if !fits {
		trace.snapshotsStopped = true
		return nil, true
	}
	trace.snapshotBytes += size
	return buildTraceStateSnapshot(state), false
}

func traceStateSnapshotBytes(state *State, maximum int) (int, bool) {
	const snapshotOverhead = 256
	used := snapshotOverhead
	add := func(amount int) bool {
		if amount < 0 || used > maximum-amount {
			return false
		}
		used += amount
		return true
	}
	directory, _ := state.dir.Data()
	stdin, _ := state.stdin.Data()
	stdout, _ := state.stdout.Data()
	stderr, _ := state.stderr.Data()
	if !add(len(directory) + len(stdin) + len(stdout) + len(stderr)) {
		return 0, false
	}
	if state.issue != nil && !add(len(state.issue.Error())) {
		return 0, false
	}
	if positional, exists := state.vars.data["@"]; exists {
		for _, value := range positional.List {
			if !add(16 + len(value)) {
				return 0, false
			}
		}
	}
	for name, value := range state.vars.data {
		if !add(64 + len(name) + traceVariableValueBytes(state.vars, name, &value)) {
			return 0, false
		}
	}
	return used, true
}

func buildTraceStateSnapshot(state *State) *TraceStateSnapshot {
	directory, directoryUnresolved := state.dir.Data()
	stdin, stdinUnresolved := state.stdin.Data()
	stdout, stdoutUnresolved := state.stdout.Data()
	stderr, stderrUnresolved := state.stderr.Data()
	exitCode, exitCodeUnresolved := state.exitStatus.Data()
	positional := state.vars.data["@"]
	snapshot := &TraceStateSnapshot{
		Directory:           directory,
		DirectoryUnresolved: directoryUnresolved,
		Args:                append([]string(nil), positional.List...),
		ArgsUnresolved:      state.vars.isUnknown("@"),
		Stdin:               string(stdin),
		StdinUnresolved:     stdinUnresolved,
		Stdout:              string(stdout),
		StdoutUnresolved:    stdoutUnresolved,
		Stderr:              string(stderr),
		StderrUnresolved:    stderrUnresolved,
		ExitCode:            exitCode,
		ExitCodeUnresolved:  exitCodeUnresolved,
		Variables:           make([]*TraceVariable, 0, len(state.vars.data)),
	}
	if state.issue != nil {
		snapshot.Error = state.issue.Error()
	}
	for _, name := range state.vars.names() {
		value := state.vars.data[name]
		snapshot.Variables = append(snapshot.Variables, &TraceVariable{
			Name:       name,
			Kind:       traceVariableKind(value.Kind),
			Value:      traceVariableValue(state.vars, name, &value),
			Exported:   value.Exported,
			ReadOnly:   value.ReadOnly,
			Unresolved: state.vars.isUnknown(name),
		})
	}
	return snapshot
}

func traceVariableKind(kind expand.ValueKind) string {
	switch kind {
	case expand.Indexed:
		return "indexed"
	case expand.Associative:
		return "associative"
	default:
		return "string"
	}
}

func traceVariableValueBytes(variables *variables, name string, value *expand.Variable) int {
	switch value.Kind {
	case expand.Indexed:
		size := 0
		for index, item := range value.List {
			if slots := variables.indexed[name]; slots != nil {
				if _, exists := slots[index]; !exists {
					continue
				}
			}
			size += len(strconv.Itoa(index)) + 1 + len(item) + 1
		}
		return size
	case expand.Associative:
		size := 0
		for key, item := range value.Map {
			size += len(key) + 1 + len(item) + 1
		}
		return size
	default:
		return len(value.Str)
	}
}

func traceVariableValue(variables *variables, name string, value *expand.Variable) string {
	switch value.Kind {
	case expand.Indexed:
		var result strings.Builder
		result.Grow(traceVariableValueBytes(variables, name, value))
		for index, item := range value.List {
			if slots := variables.indexed[name]; slots != nil {
				if _, exists := slots[index]; !exists {
					continue
				}
			}
			result.WriteString(strconv.Itoa(index))
			result.WriteByte('=')
			result.WriteString(item)
			result.WriteByte('\n')
		}
		return strings.TrimSuffix(result.String(), "\n")
	case expand.Associative:
		keys := make([]string, 0, len(value.Map))
		for key := range value.Map {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var result strings.Builder
		result.Grow(traceVariableValueBytes(variables, name, value))
		for _, key := range keys {
			result.WriteString(key)
			result.WriteByte('=')
			result.WriteString(value.Map[key])
			result.WriteByte('\n')
		}
		return strings.TrimSuffix(result.String(), "\n")
	default:
		return value.Str
	}
}

func (trace *executionTrace) commandStarted(execution *ExecutionContext, state *State, commandSyntax syntax.Command, invocation *Invocation) {
	if trace == nil || trace.observer == nil {
		return
	}
	pathID, parentPathID := tracePathIDs(state)
	nodeID := trace.nodeIDs[commandSyntax]
	if nodeID == 0 {
		nodeID = trace.lastNode[pathID]
	}
	sequence := trace.emit(&TraceEvent{
		Kind:             TraceCommandStarted,
		Node:             trace.nodes[nodeID],
		NodeID:           nodeID,
		PathID:           pathID,
		ParentPathID:     parentPathID,
		PreviousSequence: trace.lastEvent[pathID],
		Steps:            execution.executedSteps,
		Memory:           execution.traceMemoryWithAggregate(state, trace.aggregate, trace.paths),
		Invocation:       cloneTraceInvocation(invocation),
	})
	trace.lastEvent[pathID] = sequence
}

func (trace *executionTrace) commandFinished(execution *ExecutionContext, state *State, commandSyntax syntax.Command, result *CommandResult, err error) {
	if trace == nil || trace.observer == nil {
		return
	}
	pathID, parentPathID := tracePathIDs(state)
	nodeID := trace.nodeIDs[commandSyntax]
	if nodeID == 0 {
		nodeID = trace.lastNode[pathID]
	}
	event := &TraceEvent{
		Kind:             TraceCommandFinished,
		Node:             trace.nodes[nodeID],
		NodeID:           nodeID,
		PathID:           pathID,
		ParentPathID:     parentPathID,
		PreviousSequence: trace.lastEvent[pathID],
		Steps:            execution.executedSteps,
		Memory:           execution.traceMemoryWithAggregate(state, trace.aggregate, trace.paths),
		CommandResult:    trace.commandResult(result, err),
	}
	if err != nil {
		event.Error = err.Error()
	}
	trace.lastEvent[pathID] = trace.emit(event)
}

func (trace *executionTrace) pathsCompleted(execution *ExecutionContext, paths []*pathResult) {
	if trace == nil || trace.observer == nil {
		return
	}
	budget, _ := execution.newRetainedPathBudget(paths)
	for _, path := range paths {
		pathID, parentPathID := tracePathIDs(path.state)
		sequence := trace.emit(&TraceEvent{
			Kind:             TracePathCompleted,
			Node:             trace.nodes[trace.lastNode[pathID]],
			NodeID:           trace.lastNode[pathID],
			PathID:           pathID,
			ParentPathID:     parentPathID,
			PreviousSequence: trace.lastEvent[pathID],
			Steps:            execution.executedSteps,
			Memory:           execution.traceMemory(path.state, budget),
			PathResult:       tracePathResult(path.state),
			Status:           path.status,
		})
		trace.lastEvent[pathID] = sequence
	}
}

func tracePathResult(state *State) *TracePathResult {
	if state == nil {
		return &TracePathResult{}
	}
	stdout, stdoutUnresolved := state.stdout.Data()
	stderr, stderrUnresolved := state.stderr.Data()
	exitCode, exitCodeUnresolved := state.exitStatus.Data()
	result := &TracePathResult{
		Stdout:             string(stdout),
		Stderr:             string(stderr),
		ExitCode:           exitCode,
		StdoutUnresolved:   stdoutUnresolved,
		StderrUnresolved:   stderrUnresolved,
		ExitCodeUnresolved: exitCodeUnresolved,
	}
	if state.issue != nil {
		result.Error = state.issue.Error()
	}
	return result
}

func cloneTraceInvocation(invocation *Invocation) *Invocation {
	if invocation == nil {
		return nil
	}
	cloned := &Invocation{
		Name:  invocation.Name,
		Dir:   invocation.Dir,
		Stdin: append([]byte(nil), invocation.Stdin...),
	}
	if len(invocation.Args) != 0 {
		cloned.Args = make([]*Argument, len(invocation.Args))
		for index, argument := range invocation.Args {
			value := *argument
			cloned.Args[index] = &value
		}
	}
	if len(invocation.Env) != 0 {
		cloned.Env = make(map[string]string, len(invocation.Env))
		for name, value := range invocation.Env {
			cloned.Env[name] = value
		}
	}
	if invocation.Unresolved != nil {
		value := *invocation.Unresolved
		value.Env = append([]string(nil), invocation.Unresolved.Env...)
		cloned.Unresolved = &value
	}
	return cloned
}

func (trace *executionTrace) commandResult(result *CommandResult, err error) *TraceCommandResult {
	summary := traceCommandResult(result, err)
	if trace.options == nil || !trace.options.StateSnapshots || result != nil && result.operation != nil {
		return summary
	}
	if trace.snapshotsStopped {
		summary.OutputTruncated = true
		return summary
	}
	stdoutBytes, stderrBytes := 0, 0
	if result != nil {
		stdout, stderr, _, _, _, _ := commandResultValues(result)
		stdoutBytes = len(stdout)
		stderrBytes = len(stderr)
	}
	remaining := trace.options.MaxSnapshotBytes - trace.snapshotBytes
	if stdoutBytes > remaining || stderrBytes > remaining-stdoutBytes {
		trace.snapshotsStopped = true
		summary.OutputTruncated = true
		return summary
	}
	trace.snapshotBytes += stdoutBytes + stderrBytes
	summary.OutputCaptured = true
	if result != nil {
		stdout, stderr, _, _, _, _ := commandResultValues(result)
		summary.Stdout = string(stdout)
		summary.Stderr = string(stderr)
	}
	return summary
}

func traceCommandResult(result *CommandResult, err error) *TraceCommandResult {
	if result == nil {
		if err != nil {
			return &TraceCommandResult{ExitCodeUnresolved: true}
		}
		return &TraceCommandResult{
			StdoutUnresolved:   true,
			StderrUnresolved:   true,
			ExitCodeUnresolved: true,
			Unresolved:         true,
		}
	}
	stdout, stderr, exitCode, stdoutUnresolved, stderrUnresolved, exitCodeUnresolved := commandResultValues(result)
	return &TraceCommandResult{
		ExitCode:           exitCode,
		StdoutBytes:        len(stdout),
		StderrBytes:        len(stderr),
		Action:             result.Action,
		StdoutUnresolved:   stdoutUnresolved,
		StderrUnresolved:   stderrUnresolved,
		ExitCodeUnresolved: exitCodeUnresolved,
		Unresolved:         stdoutUnresolved && stderrUnresolved && exitCodeUnresolved,
	}
}

func traceStatementKind(statement *syntax.Stmt) string {
	if statement == nil {
		return "statement"
	}
	switch statement.Cmd.(type) {
	case *syntax.CallExpr:
		return "command"
	case *syntax.IfClause, *syntax.CaseClause, *syntax.TestClause:
		return "condition"
	case *syntax.ForClause, *syntax.WhileClause:
		return "loop"
	case *syntax.BinaryCmd:
		return "operator"
	case *syntax.Subshell:
		return "subshell"
	case *syntax.Block:
		return "block"
	case *syntax.FuncDecl:
		return "function"
	case *syntax.ArithmCmd:
		return "arithmetic"
	case *syntax.DeclClause:
		return "declaration"
	case *syntax.LetClause:
		return "arithmetic"
	default:
		return "statement"
	}
}

func traceSource(name string, node syntax.Node) *TraceSource {
	start := node.Pos()
	end := node.End()
	return &TraceSource{
		Name:      name,
		Line:      start.Line(),
		Column:    start.Col(),
		EndLine:   end.Line(),
		EndColumn: end.Col(),
	}
}

func traceSnippet(source string, node syntax.Node) string {
	start := node.Pos()
	end := node.End()
	if !start.IsValid() || !end.IsValid() || start.Offset() >= uint(len(source)) || end.Offset() <= start.Offset() {
		return traceStatementKind(node.(*syntax.Stmt))
	}
	endOffset := min(int(end.Offset()), len(source))
	value := strings.Join(strings.Fields(source[int(start.Offset()):endOffset]), " ")
	const maximumBytes = 160
	if len(value) <= maximumBytes {
		return value
	}
	value = value[:maximumBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "…"
}
