package runtime

import (
	"errors"
	"reflect"
	"testing"
)

func TestTracePathAndInvocationCopies(t *testing.T) {
	if got := tracePathResult(nil); !reflect.DeepEqual(got, &TracePathResult{}) {
		t.Fatalf("nil path result = %#v", got)
	}
	state := newState(&Request{}, defaultMaxMemoryBytes)
	state.stdout = newUnresolved([]byte("representative stdout"))
	state.stderr = newCertain([]byte("stderr"))
	state.exitStatus = newUnresolved(7)
	state.issue = errors.New("failed")
	result := tracePathResult(state)
	if result.Stdout != "representative stdout" || !result.StdoutUnresolved || result.Stderr != "stderr" || result.StderrUnresolved || result.ExitCode != 7 || !result.ExitCodeUnresolved || result.Error != "failed" {
		t.Fatalf("path result = %#v", result)
	}

	if cloneTraceInvocation(nil) != nil {
		t.Fatal("nil invocation clone is non-nil")
	}
	original := &Invocation{
		Name:  "command",
		Args:  []*Argument{{Kind: ArgumentString, Value: "argument"}},
		Env:   map[string]string{"VALUE": "original"},
		Dir:   "/work",
		Stdin: []byte("input"),
		Unresolved: &InvocationUnresolved{
			Env: []string{"UNKNOWN"},
		},
	}
	clone := cloneTraceInvocation(original)
	clone.Args[0].Value = "changed"
	clone.Env["VALUE"] = "changed"
	clone.Stdin[0] = 'X'
	clone.Unresolved.Env[0] = "CHANGED"
	if original.Args[0].Value != "argument" || original.Env["VALUE"] != "original" || string(original.Stdin) != "input" || original.Unresolved.Env[0] != "UNKNOWN" {
		t.Fatalf("invocation clone shares storage: %#v", original)
	}
}

func TestTraceCommandResultRepresentations(t *testing.T) {
	if got := traceCommandResult(nil, errors.New("failed")); !got.ExitCodeUnresolved || got.Unresolved {
		t.Fatalf("error-only command result = %#v", got)
	}
	if got := traceCommandResult(nil, nil); !got.Unresolved || !got.StdoutUnresolved || !got.StderrUnresolved || !got.ExitCodeUnresolved {
		t.Fatalf("nil command result = %#v", got)
	}
	resolved := &CommandResult{Action: CommandStop, outputs: []*CommandOutput{{
		Stdout:   newCertain([]byte("stdout")),
		Stderr:   newCertain([]byte("stderr")),
		ExitCode: newCertain(3),
	}}}
	summary := traceCommandResult(resolved, nil)
	if summary.StdoutBytes != 6 || summary.StderrBytes != 6 || summary.ExitCode != 3 || summary.Action != CommandStop || summary.Unresolved {
		t.Fatalf("resolved command summary = %#v", summary)
	}
	unresolved := &CommandResult{outputs: []*CommandOutput{{
		Stdout:   newUnresolved([]byte("out")),
		Stderr:   newUnresolved([]byte("err")),
		ExitCode: newUnresolved(0),
	}}}
	if got := traceCommandResult(unresolved, nil); !got.Unresolved {
		t.Fatalf("unresolved command summary = %#v", got)
	}
}

func TestExecutionTraceCommandOutputCaptureBudget(t *testing.T) {
	result := &CommandResult{outputs: []*CommandOutput{{
		Stdout:   newCertain([]byte("stdout")),
		Stderr:   newCertain([]byte("stderr")),
		ExitCode: newCertain(0),
	}}}
	trace := &executionTrace{}
	if got := trace.commandResult(result, nil); got.OutputCaptured || got.OutputTruncated {
		t.Fatalf("default capture = %#v", got)
	}

	trace.options = &TraceOptions{StateSnapshots: true, MaxSnapshotBytes: 32}
	got := trace.commandResult(result, nil)
	if !got.OutputCaptured || got.Stdout != "stdout" || got.Stderr != "stderr" || got.OutputTruncated {
		t.Fatalf("captured output = %#v", got)
	}
	trace.options.MaxSnapshotBytes = trace.snapshotBytes
	got = trace.commandResult(result, nil)
	if !got.OutputTruncated || got.OutputCaptured || !trace.snapshotsStopped {
		t.Fatalf("budget-truncated output = %#v", got)
	}
	got = trace.commandResult(result, nil)
	if !got.OutputTruncated {
		t.Fatalf("capture after stop = %#v", got)
	}

	operation := &CommandResult{operation: &commandOperation{}}
	trace = &executionTrace{options: &TraceOptions{StateSnapshots: true, MaxSnapshotBytes: 32}}
	if got := trace.commandResult(operation, nil); got.OutputCaptured {
		t.Fatalf("declarative operation output was captured: %#v", got)
	}
}

func TestTracePathIdentityHelpers(t *testing.T) {
	if path, parent := tracePathIDs(nil); path != 0 || parent != 0 {
		t.Fatalf("nil path IDs = %d, %d", path, parent)
	}
	parent := newState(&Request{}, defaultMaxMemoryBytes)
	parent.pathID = 10
	child := parent.clone()
	child.pathID = 11
	child.parent = parent
	if path, parentID := tracePathIDs(child); path != 11 || parentID != 10 {
		t.Fatalf("child path IDs = %d, %d", path, parentID)
	}
	if (&executionTrace{}).currentNodeID(nil) != 0 || (*executionTrace)(nil).nodeID(nil) != 0 {
		t.Fatal("nil trace identity helpers returned a node")
	}
}
