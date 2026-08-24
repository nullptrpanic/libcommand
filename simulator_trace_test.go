package libcommand_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	libcommand "github.com/nullptrpanic/libcommand"
)

func TestSimulateTraceReportsConcreteFinalPathOutput(t *testing.T) {
	simulator := libcommand.NewBuilder().Build()
	var completed *libcommand.TraceEvent
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
		Source: "printf out; printf err >&2; false",
	}, func(event *libcommand.TraceEvent) bool {
		if event.Kind == libcommand.TracePathCompleted {
			completed = event
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed == nil || completed.PathResult == nil {
		t.Fatalf("completed event = %#v, want path result", completed)
	}
	result := completed.PathResult
	if result.Stdout != "out" || result.Stderr != "err" || result.ExitCode != 1 || result.Error != "" {
		t.Fatalf("path result = %#v", result)
	}
	if result.StdoutUnresolved || result.StderrUnresolved || result.ExitCodeUnresolved {
		t.Fatalf("path result unexpectedly unresolved: %#v", result)
	}
}

func TestSimulateTraceReactivatesLoopForEveryIteration(t *testing.T) {
	tests := []*struct {
		name        string
		source      string
		stdin       string
		reactivated int
	}{
		{name: "word for", source: "for item in one two three; do :; done", reactivated: 2},
		{name: "arithmetic for", source: "for ((i=0; i<3; i++)); do :; done", reactivated: 2},
		{name: "while", source: "i=0; while ((i<3)); do ((i++)); done", reactivated: 2},
		{name: "until", source: "i=0; until ((i>=3)); do ((i++)); done", reactivated: 2},
		{name: "select", source: "select item in one; do break; done", stdin: "\n1\n", reactivated: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			simulator := libcommand.NewBuilder().Build()
			started := 0
			reactivated := 0
			err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
				Source: test.source,
				Stdin:  []byte(test.stdin),
			}, func(event *libcommand.TraceEvent) bool {
				if event.Node == nil || event.Node.Kind != "loop" {
					return true
				}
				switch event.Kind {
				case libcommand.TraceStatementStarted:
					started++
				case libcommand.TraceStatementActivated:
					reactivated++
				}
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			if started != 1 || reactivated != test.reactivated {
				t.Fatalf("loop events = %d started + %d reactivated, want 1 + %d", started, reactivated, test.reactivated)
			}
		})
	}
}

func TestSimulateTraceStartsNestedConditionForEveryLoopIteration(t *testing.T) {
	simulator := libcommand.NewBuilder().Build()
	loopStarted := 0
	loopActivated := 0
	conditionStarted := 0
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
		Source: "for i in one two three; do if [[ $i = one ]]; then :; else :; fi; done",
	}, func(event *libcommand.TraceEvent) bool {
		if event.Node == nil {
			return true
		}
		switch {
		case event.Node.Kind == "loop" && event.Kind == libcommand.TraceStatementStarted:
			loopStarted++
		case event.Node.Kind == "loop" && event.Kind == libcommand.TraceStatementActivated:
			loopActivated++
		case event.Node.Kind == "condition" && !event.Node.Embedded && event.Kind == libcommand.TraceStatementStarted:
			conditionStarted++
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if loopStarted != 1 || loopActivated != 2 || conditionStarted != 3 {
		t.Fatalf("control events = loop %d started + %d activated, condition %d started; want 1 + 2 and 3", loopStarted, loopActivated, conditionStarted)
	}
}

func TestSimulateTraceMarksUnresolvedFinalPathOutput(t *testing.T) {
	simulator := libcommand.NewBuilder().Build()
	var result *libcommand.TracePathResult
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
		Source: "unknown-command",
	}, func(event *libcommand.TraceEvent) bool {
		if event.Kind == libcommand.TracePathCompleted {
			result = event.PathResult
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.StdoutUnresolved || !result.StderrUnresolved || !result.ExitCodeUnresolved {
		t.Fatalf("path result = %#v, want unresolved streams and exit code", result)
	}
}

func TestSimulateTraceReportsFinalPathError(t *testing.T) {
	simulator := libcommand.NewBuilder().Command("fail",
		func(context.Context, *libcommand.CommandContext, *libcommand.Invocation) (*libcommand.CommandResult, error) {
			return nil, errors.New("command failed")
		}).Build()
	var result *libcommand.TracePathResult
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
		Source: "fail",
	}, func(event *libcommand.TraceEvent) bool {
		if event.Kind == libcommand.TracePathCompleted {
			result = event.PathResult
		}
		return true
	})
	if err == nil || !strings.Contains(err.Error(), "command failed") {
		t.Fatalf("error = %v, want command failure", err)
	}
	if result == nil || !strings.Contains(result.Error, "command failed") {
		t.Fatalf("path result = %#v, want command failure", result)
	}
}

func TestSimulateTraceWithOptionsIncludesStatementStateSnapshots(t *testing.T) {
	simulator := libcommand.NewBuilder().Build()
	var started, finished *libcommand.TraceStateSnapshot
	err := simulator.SimulateTraceWithOptions(context.Background(), &libcommand.SimulationRequest{
		Source: "printf out",
		Env:    map[string]string{"TOKEN": "secret"},
		Args:   []string{"first"},
		Stdin:  []byte("input\n"),
	}, &libcommand.TraceOptions{
		StateSnapshots:   true,
		MaxSnapshotBytes: 1 << 20,
	}, func(event *libcommand.TraceEvent) bool {
		switch event.Kind {
		case libcommand.TraceStatementStarted:
			started = event.Snapshot
		case libcommand.TraceStatementFinished:
			finished = event.Snapshot
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if started == nil || finished == nil {
		t.Fatalf("snapshots = started %#v, finished %#v", started, finished)
	}
	if started.Directory != "/" || started.DirectoryUnresolved || started.Stdin != "input\n" || started.StdinUnresolved {
		t.Fatalf("input snapshot = %#v", started)
	}
	if !reflect.DeepEqual(started.Args, []string{"first"}) || started.ArgsUnresolved {
		t.Fatalf("snapshot args = %#v, unresolved = %v", started.Args, started.ArgsUnresolved)
	}
	var token *libcommand.TraceVariable
	for _, variable := range started.Variables {
		if variable.Name == "TOKEN" {
			token = variable
			break
		}
	}
	if token == nil || token.Value != "secret" || !token.Exported || token.Unresolved {
		t.Fatalf("TOKEN snapshot = %#v", token)
	}
	if finished.Stdout != "out" || finished.StdoutUnresolved || finished.ExitCode != 0 || finished.ExitCodeUnresolved {
		t.Fatalf("output snapshot = %#v", finished)
	}
}

func TestSimulateTraceSnapshotBudgetStopsSnapshotRetention(t *testing.T) {
	simulator := libcommand.NewBuilder().Build()
	var statement *libcommand.TraceEvent
	err := simulator.SimulateTraceWithOptions(context.Background(), &libcommand.SimulationRequest{
		Source: ":",
	}, &libcommand.TraceOptions{
		StateSnapshots:   true,
		MaxSnapshotBytes: 1,
	}, func(event *libcommand.TraceEvent) bool {
		if event.Kind == libcommand.TraceStatementStarted {
			statement = event
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if statement == nil || statement.Snapshot != nil || !statement.SnapshotTruncated {
		t.Fatalf("statement event = %#v, want omitted snapshot with truncation", statement)
	}
}

func TestSimulateTraceDoesNotCaptureStateByDefault(t *testing.T) {
	simulator := libcommand.NewBuilder().Build()
	var statement *libcommand.TraceEvent
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{Source: ":"}, func(event *libcommand.TraceEvent) bool {
		if event.Kind == libcommand.TraceStatementStarted {
			statement = event
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if statement == nil || statement.Snapshot != nil || statement.SnapshotTruncated {
		t.Fatalf("statement event = %#v, want snapshots disabled", statement)
	}
}

func TestSimulateTraceDiscoversUnexecutedBranchSyntax(t *testing.T) {
	simulator := libcommand.NewBuilder().Build()
	var events []*libcommand.TraceEvent
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
		Source: "if true; then\n  echo yes\nelse\n  echo no\nfi\n",
	}, func(event *libcommand.TraceEvent) bool {
		events = append(events, event)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}

	var discovered []string
	for _, event := range events {
		if event.Kind == libcommand.TraceNodeDiscovered {
			discovered = append(discovered, event.Node.Snippet)
		}
	}
	joined := strings.Join(discovered, "\n")
	for _, snippet := range []string{"if true", "echo yes", "echo no"} {
		if !strings.Contains(joined, snippet) {
			t.Fatalf("discovered nodes = %q, want snippet %q", joined, snippet)
		}
	}
	if len(events) == 0 || events[0].Kind != libcommand.TraceSimulationStarted {
		t.Fatalf("first event = %#v, want simulation started", events)
	}
	if events[len(events)-1].Kind != libcommand.TraceSimulationFinished {
		t.Fatalf("last event = %#v, want simulation finished", events[len(events)-1])
	}
}

func TestSimulateTraceMarksOnlyExecutedKnownBranch(t *testing.T) {
	var events []*libcommand.TraceEvent
	simulator := libcommand.NewBuilder().Build()
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
		Source: "if true; then\n  echo yes\nelse\n  echo no\nfi\n",
	}, collectTrace(&events))
	if err != nil {
		t.Fatal(err)
	}

	nodeIDs := make(map[string]uint64)
	executed := make(map[uint64]bool)
	for _, event := range events {
		switch event.Kind {
		case libcommand.TraceNodeDiscovered:
			nodeIDs[event.Node.Snippet] = event.Node.ID
		case libcommand.TraceStatementStarted:
			executed[event.NodeID] = true
		}
	}
	if !executed[nodeIDs["echo yes"]] {
		t.Fatal("known true branch was not traced as executed")
	}
	if executed[nodeIDs["echo no"]] {
		t.Fatal("known false branch was traced as executed")
	}
}

func TestSimulateTraceRecordsUnresolvedPathFork(t *testing.T) {
	var events []*libcommand.TraceEvent
	statePaths := make(map[string]uint64)
	simulator := libcommand.NewBuilder().
		Command("record", func(_ context.Context, command *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
			statePaths[invocation.Args[0].Value] = command.State().PathID()
			return &libcommand.CommandResult{}, nil
		}).
		Build()
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
		Source: "if unknown-command; then\n  record yes\nelse\n  record no\nfi\n",
	}, collectTrace(&events))
	if err != nil {
		t.Fatal(err)
	}

	var children []uint64
	var forkSequence uint64
	var forkEvent *libcommand.TraceEvent
	branchPaths := make(map[uint64]bool)
	branchParents := make(map[uint64]uint64)
	for _, event := range events {
		if event.Kind == libcommand.TracePathForked && len(event.ChildPathIDs) == 2 {
			children = event.ChildPathIDs
			forkSequence = event.Sequence
			forkEvent = event
		}
		if event.Kind == libcommand.TraceStatementStarted && event.Node != nil &&
			(event.Node.Snippet == "record yes" || event.Node.Snippet == "record no") {
			branchPaths[event.PathID] = true
			branchParents[event.PathID] = event.ParentPathID
		}
	}
	if len(children) != 2 {
		t.Fatalf("fork children = %#v, want two paths", children)
	}
	if len(branchPaths) != 2 || !branchPaths[children[0]] || !branchPaths[children[1]] {
		t.Fatalf("executed branch paths = %#v, fork children = %#v", branchPaths, children)
	}
	if len(statePaths) != 2 || !branchPaths[statePaths["yes"]] || !branchPaths[statePaths["no"]] {
		t.Fatalf("state paths = %#v, traced branch paths = %#v", statePaths, branchPaths)
	}
	if forkEvent == nil || forkEvent.ParentPathID != 0 {
		t.Fatalf("root fork event = %#v, want no parent", forkEvent)
	}
	for _, child := range children {
		if branchParents[child] != forkEvent.PathID {
			t.Fatalf("child %d parent = %d, want %d", child, branchParents[child], forkEvent.PathID)
		}
	}
	firstChildEvent := make(map[uint64]*libcommand.TraceEvent)
	for _, event := range events {
		if event.Sequence <= forkSequence || event.PathID == 0 || firstChildEvent[event.PathID] != nil {
			continue
		}
		for _, child := range children {
			if event.PathID == child {
				firstChildEvent[child] = event
			}
		}
	}
	for _, child := range children {
		if event := firstChildEvent[child]; event == nil || event.PreviousSequence != forkSequence {
			t.Fatalf("first event for child %d = %#v, want previous sequence %d", child, event, forkSequence)
		}
	}
}

func TestSimulateTraceCopiesTypedCommandInvocation(t *testing.T) {
	var events []*libcommand.TraceEvent
	simulator := libcommand.NewBuilder().Command("record",
		func(context.Context, *libcommand.CommandContext, *libcommand.Invocation) (*libcommand.CommandResult, error) {
			return &libcommand.CommandResult{}, nil
		}).Build()
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
		Source: "value=$(unknown-command); record \"$value\" concrete",
	}, collectTrace(&events))
	if err != nil {
		t.Fatal(err)
	}

	var invocation *libcommand.Invocation
	for _, event := range events {
		if event.Kind == libcommand.TraceCommandStarted && event.Invocation != nil && event.Invocation.Name == "record" {
			invocation = event.Invocation
		}
	}
	if invocation == nil {
		t.Fatal("record command invocation was not traced")
	}
	want := []*libcommand.Argument{{Kind: libcommand.ArgumentUnresolved}, {Kind: libcommand.ArgumentString, Value: "concrete"}}
	if !reflect.DeepEqual(invocation.Args, want) {
		t.Fatalf("arguments = %#v, want %#v", invocation.Args, want)
	}
}

func TestSimulateTraceAssociatesPipelineCommandsWithTheirSyntaxNodes(t *testing.T) {
	var events []*libcommand.TraceEvent
	simulator := libcommand.NewBuilder().Build()
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
		Source: "echo bGFyay1jbGk= | base64 -d",
	}, collectTrace(&events))
	if err != nil {
		t.Fatal(err)
	}

	wantSnippet := map[string]string{
		"echo":   "echo bGFyay1jbGk=",
		"base64": "base64 -d",
	}
	seen := make(map[string]uint64)
	for _, event := range events {
		if event.Kind != libcommand.TraceCommandStarted || event.Invocation == nil {
			continue
		}
		snippet, relevant := wantSnippet[event.Invocation.Name]
		if !relevant {
			continue
		}
		if event.Node == nil || event.Node.Snippet != snippet {
			t.Fatalf("%s trace node = %#v, want snippet %q", event.Invocation.Name, event.Node, snippet)
		}
		if !event.Node.Embedded {
			t.Fatalf("%s pipeline operand was not marked embedded: %#v", event.Invocation.Name, event.Node)
		}
		seen[event.Invocation.Name] = event.NodeID
	}
	if len(seen) != len(wantSnippet) || seen["echo"] == seen["base64"] {
		t.Fatalf("pipeline command node IDs = %#v, want two distinct syntax nodes", seen)
	}
}

func TestSimulateTraceAssociatesExpandedCommandWithOuterCallSyntax(t *testing.T) {
	var events []*libcommand.TraceEvent
	simulator := libcommand.NewBuilder().Command("lark-cli",
		func(context.Context, *libcommand.CommandContext, *libcommand.Invocation) (*libcommand.CommandResult, error) {
			return &libcommand.CommandResult{}, nil
		}).Build()
	const source = `"$(echo bGFyay1jbGk= | base64 -d)" value`
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
		Source: source,
	}, collectTrace(&events))
	if err != nil {
		t.Fatal(err)
	}

	for _, event := range events {
		if event.Kind != libcommand.TraceCommandStarted || event.Invocation == nil || event.Invocation.Name != "lark-cli" {
			continue
		}
		if event.Node == nil || event.Node.Snippet != source {
			t.Fatalf("expanded command trace node = %#v, want outer call %q", event.Node, source)
		}
		return
	}
	t.Fatal("lark-cli command start was not traced")
}

func TestSimulateTraceReportsVirtualFileMemory(t *testing.T) {
	var events []*libcommand.TraceEvent
	simulator := libcommand.NewBuilder().Command("record",
		func(context.Context, *libcommand.CommandContext, *libcommand.Invocation) (*libcommand.CommandResult, error) {
			return &libcommand.CommandResult{}, nil
		}).Build()
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
		Source: "printf payload >file\nrecord",
	}, collectTrace(&events))
	if err != nil {
		t.Fatal(err)
	}

	var memory *libcommand.TraceMemory
	previousSequence := uint64(0)
	for _, event := range events {
		if event.Sequence <= previousSequence {
			t.Fatalf("event sequence %d follows %d", event.Sequence, previousSequence)
		}
		previousSequence = event.Sequence
		if event.Kind == libcommand.TraceStatementStarted && event.Node != nil && event.Node.Snippet == "record" {
			memory = event.Memory
		}
	}
	if memory == nil || memory.VirtualFileBytes == 0 {
		t.Fatalf("record memory = %#v, want virtual file bytes", memory)
	}
	if memory.AggregateBytes == 0 || memory.MaximumBytes != 2<<20 || memory.RetainedPaths != 1 {
		t.Fatalf("record aggregate memory = %#v", memory)
	}
}

func TestSimulateTraceObserverCanStopWithoutStoppingSimulation(t *testing.T) {
	calls := 0
	simulator := libcommand.NewBuilder().Command("record",
		func(context.Context, *libcommand.CommandContext, *libcommand.Invocation) (*libcommand.CommandResult, error) {
			calls++
			return &libcommand.CommandResult{}, nil
		}).Build()
	events := 0
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
		Source: "record; record",
	}, func(*libcommand.TraceEvent) bool {
		events++
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("trace events = %d, want 1", events)
	}
	if calls != 2 {
		t.Fatalf("command calls = %d, want 2", calls)
	}
}

func TestSimulateTraceReportsCommandAndPathCompletion(t *testing.T) {
	var events []*libcommand.TraceEvent
	simulator := libcommand.NewBuilder().Command("record",
		func(context.Context, *libcommand.CommandContext, *libcommand.Invocation) (*libcommand.CommandResult, error) {
			return &libcommand.CommandResult{Stdout: []byte("done"), ExitCode: 7}, nil
		}).Build()
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{Source: "record"}, collectTrace(&events))
	if err != nil {
		t.Fatal(err)
	}

	var kinds []libcommand.TraceEventKind
	var result *libcommand.TraceCommandResult
	for _, event := range events {
		kinds = append(kinds, event.Kind)
		if event.Kind == libcommand.TraceCommandFinished {
			result = event.CommandResult
		}
	}
	wantOrder := []libcommand.TraceEventKind{
		libcommand.TraceSimulationStarted,
		libcommand.TraceNodeDiscovered,
		libcommand.TraceStatementStarted,
		libcommand.TraceCommandStarted,
		libcommand.TraceCommandFinished,
		libcommand.TraceStatementFinished,
		libcommand.TracePathCompleted,
		libcommand.TraceSimulationFinished,
	}
	if !reflect.DeepEqual(kinds, wantOrder) {
		t.Fatalf("event kinds = %#v, want %#v", kinds, wantOrder)
	}
	if result == nil || result.ExitCode != 7 || result.StdoutBytes != 4 || result.Unresolved {
		t.Fatalf("command result = %#v", result)
	}
	if result.OutputCaptured || result.Stdout != "" || result.Stderr != "" {
		t.Fatalf("lightweight command output = %#v, want byte counts without copied streams", result)
	}
	previous := uint64(0)
	for _, event := range events {
		if event.PathID == 0 {
			continue
		}
		if event.PreviousSequence != previous {
			t.Fatalf("event %d previous sequence = %d, want %d", event.Sequence, event.PreviousSequence, previous)
		}
		previous = event.Sequence
	}
}

func TestSimulateTraceOverallUnresolvedRequiresEveryResultDimension(t *testing.T) {
	var result *libcommand.TraceCommandResult
	simulator := libcommand.NewBuilder().Command("partial",
		func(_ context.Context, command *libcommand.CommandContext, _ *libcommand.Invocation) (*libcommand.CommandResult, error) {
			return command.ResultUnknown(&libcommand.CommandResult{}, true, false, false), nil
		}).Build()
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{Source: "partial"}, func(event *libcommand.TraceEvent) bool {
		if event.Kind == libcommand.TraceCommandFinished {
			result = event.CommandResult
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.StdoutUnresolved || result.StderrUnresolved || result.ExitCodeUnresolved {
		t.Fatalf("command result = %#v, want only stdout unresolved", result)
	}
	if result.Unresolved {
		t.Fatalf("command result = %#v, partially unresolved result must not be wholly unresolved", result)
	}
}

func TestCommandContextUnresolvedResultMarksEveryResultDimension(t *testing.T) {
	var result *libcommand.TraceCommandResult
	simulator := libcommand.NewBuilder().Command("unknown",
		func(_ context.Context, command *libcommand.CommandContext, _ *libcommand.Invocation) (*libcommand.CommandResult, error) {
			return command.UnresolvedResult(), nil
		}).Build()
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{Source: "unknown"}, func(event *libcommand.TraceEvent) bool {
		if event.Kind == libcommand.TraceCommandFinished {
			result = event.CommandResult
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.StdoutUnresolved || !result.StderrUnresolved || !result.ExitCodeUnresolved || !result.Unresolved {
		t.Fatalf("command result = %#v, want every dimension unresolved", result)
	}
}

func TestSimulateTraceErrorResultIsNotWhollyUnresolved(t *testing.T) {
	var result *libcommand.TraceCommandResult
	simulator := libcommand.NewBuilder().Command("fail",
		func(context.Context, *libcommand.CommandContext, *libcommand.Invocation) (*libcommand.CommandResult, error) {
			return nil, errors.New("failed")
		}).Build()
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{Source: "fail"}, func(event *libcommand.TraceEvent) bool {
		if event.Kind == libcommand.TraceCommandFinished {
			result = event.CommandResult
		}
		return true
	})
	if err == nil {
		t.Fatal("SimulateTrace() error = nil, want handler error")
	}
	if result == nil || result.StdoutUnresolved || result.StderrUnresolved || !result.ExitCodeUnresolved {
		t.Fatalf("command result = %#v, want only exit code unresolved", result)
	}
	if result.Unresolved {
		t.Fatalf("command result = %#v, error result must not be wholly unresolved", result)
	}
}

func TestSimulateTraceSnapshotsCaptureCurrentCommandOutput(t *testing.T) {
	simulator := libcommand.NewBuilder().Command("unknown-command",
		func(_ context.Context, command *libcommand.CommandContext, _ *libcommand.Invocation) (*libcommand.CommandResult, error) {
			return command.UnresolvedResult(), nil
		}).Build()
	var unknownResult, echoResult *libcommand.TraceCommandResult
	activeCommands := make(map[uint64]string)
	err := simulator.SimulateTraceWithOptions(context.Background(), &libcommand.SimulationRequest{
		Source: "unknown-command\necho 'simulation complete'",
	}, &libcommand.TraceOptions{
		StateSnapshots:   true,
		MaxSnapshotBytes: 1 << 20,
	}, func(event *libcommand.TraceEvent) bool {
		if event.Kind == libcommand.TraceCommandStarted && event.Invocation != nil {
			activeCommands[event.PathID] = event.Invocation.Name
			return true
		}
		if event.Kind != libcommand.TraceCommandFinished {
			return true
		}
		switch activeCommands[event.PathID] {
		case "unknown-command":
			unknownResult = event.CommandResult
		case "echo":
			echoResult = event.CommandResult
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if unknownResult == nil || !unknownResult.OutputCaptured || !unknownResult.StdoutUnresolved || !unknownResult.StderrUnresolved || !unknownResult.ExitCodeUnresolved {
		t.Fatalf("unknown command result = %#v", unknownResult)
	}
	if echoResult == nil || !echoResult.OutputCaptured || echoResult.OutputTruncated {
		t.Fatalf("echo command result = %#v, want captured output", echoResult)
	}
	if echoResult.Stdout != "simulation complete\n" || echoResult.Stderr != "" || echoResult.ExitCode != 0 {
		t.Fatalf("echo command result = %#v", echoResult)
	}
	if echoResult.StdoutUnresolved || echoResult.StderrUnresolved || echoResult.ExitCodeUnresolved || echoResult.Unresolved {
		t.Fatalf("echo command certainty = %#v, want concrete streams and status", echoResult)
	}
}

func collectTrace(events *[]*libcommand.TraceEvent) libcommand.TraceObserver {
	return func(event *libcommand.TraceEvent) bool {
		*events = append(*events, event)
		return true
	}
}

func TestSimulateTraceDiscoversDynamicEvalSyntax(t *testing.T) {
	simulator := libcommand.NewBuilder().Build()
	var dynamic []*libcommand.TraceNode
	err := simulator.SimulateTrace(context.Background(), &libcommand.SimulationRequest{
		Source: "eval 'echo dynamic'",
	}, func(event *libcommand.TraceEvent) bool {
		if event.Kind == libcommand.TraceNodeDiscovered && event.Node.Source.Name == "eval" {
			dynamic = append(dynamic, event.Node)
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dynamic) != 1 || dynamic[0].Snippet != "echo dynamic" || dynamic[0].Embedded {
		t.Fatalf("dynamic nodes = %#v, want one eval node", dynamic)
	}
}
