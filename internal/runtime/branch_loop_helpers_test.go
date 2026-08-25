package runtime

import (
	"context"
	"testing"
)

func TestCollectInactivePathClassification(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 10, &Request{})
	active := &pathResult{state: state, status: StatusCompleted}
	var completed []*pathResult
	if execution.collectInactivePath(&completed, active) || len(completed) != 0 {
		t.Fatal("active path was collected")
	}

	incomplete := &pathResult{state: state.clone(), status: StatusIncomplete}
	if !execution.collectInactivePath(&completed, incomplete) || len(completed) != 1 || completed[0] != incomplete {
		t.Fatalf("incomplete collection = %#v", completed)
	}

	execution.stop = true
	stopped := &pathResult{state: state.clone(), status: StatusCompleted}
	if !execution.collectInactivePath(&completed, stopped) || stopped.status != StatusTerminated {
		t.Fatalf("stopped path = %#v", stopped)
	}
}

func TestSelectInputConsumption(t *testing.T) {
	state := newState(&Request{}, defaultMaxMemoryBytes)
	if line, ok := consumeSelectInput(state); ok || line != "" {
		t.Fatalf("empty input = %q, %v", line, ok)
	}
	state.stdin = newCertain([]byte("final line"))
	if line, ok := consumeSelectInput(state); !ok || line != "final line" {
		t.Fatalf("unterminated input = %q, %v", line, ok)
	}
	if remaining, unresolved := state.stdin.Data(); unresolved || len(remaining) != 0 {
		t.Fatalf("remaining input = %q, unresolved=%v", remaining, unresolved)
	}
}

func TestCollectLoopContinuationSignals(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 10, &Request{})
	tests := []*struct {
		name          string
		status        Status
		signal        controlSignal
		depth         int
		wantActive    int
		wantCompleted int
		wantSignal    controlSignal
		wantDepth     int
	}{
		{name: "incomplete", status: StatusIncomplete, wantCompleted: 1},
		{name: "break nested", status: StatusCompleted, signal: signalBreak, depth: 2, wantCompleted: 1, wantSignal: signalBreak, wantDepth: 1},
		{name: "break current", status: StatusCompleted, signal: signalBreak, depth: 1, wantCompleted: 1},
		{name: "continue nested", status: StatusCompleted, signal: signalContinue, depth: 2, wantCompleted: 1, wantSignal: signalContinue, wantDepth: 1},
		{name: "continue current", status: StatusCompleted, signal: signalContinue, depth: 1, wantActive: 1},
		{name: "ordinary", status: StatusCompleted, signal: signalNone, wantActive: 1},
		{name: "return", status: StatusCompleted, signal: signalReturn, wantCompleted: 1, wantSignal: signalReturn},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := &pathResult{state: state.clone(), status: test.status}
			result.state.signal = test.signal
			result.state.signalDepth = test.depth
			var active []*pathResult
			var completed []*pathResult
			execution.collectLoopContinuation(&active, &completed, result)
			if len(active) != test.wantActive || len(completed) != test.wantCompleted || result.state.signal != test.wantSignal || result.state.signalDepth != test.wantDepth {
				t.Fatalf("active=%#v completed=%#v signal=%v depth=%d", active, completed, result.state.signal, result.state.signalDepth)
			}
		})
	}
}
