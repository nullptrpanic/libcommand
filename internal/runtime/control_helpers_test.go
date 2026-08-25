package runtime

import (
	"context"
	"reflect"
	"testing"

	"mvdan.cc/sh/v3/expand"
)

func TestVariableRollbackRestoresValueCertaintySlotsAndVersion(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 20, &Request{})
	state.vars.putIndexedWithCertainty("value", expand.Variable{Set: true, Kind: expand.Indexed, List: []string{"zero", "", "two"}}, true, map[int]struct{}{0: {}, 2: {}})
	originalVersion := state.vars.versions["value"]
	execution.statementDepth = 1
	execution.recordVariableRollback(state, "value")
	state.vars.put("value", expand.Variable{Set: true, Kind: expand.String, Str: "changed"})
	execution.rollbackVariables(0)
	got := state.vars.Get("value")
	if got.Kind != expand.Indexed || !reflect.DeepEqual(got.List, []string{"zero", "", "two"}) || !state.vars.isUnknown("value") || !reflect.DeepEqual(state.vars.indexedSlots("value"), map[int]struct{}{0: {}, 2: {}}) || state.vars.versions["value"] != originalVersion {
		t.Fatalf("restored value = %#v, unknown=%v, slots=%#v, version=%d", got, state.vars.isUnknown("value"), state.vars.indexedSlots("value"), state.vars.versions["value"])
	}
	if len(execution.variableRollbacks) != 0 || execution.variableRollbackBytes != 0 {
		t.Fatalf("rollback log retained: %#v, %d", execution.variableRollbacks, execution.variableRollbackBytes)
	}

	execution.recordVariableRollback(state, "new")
	state.vars.put("new", expand.Variable{Set: true, Kind: expand.String, Str: "temporary"})
	execution.rollbackVariables(0)
	if state.vars.Get("new").IsSet() {
		t.Fatal("new variable survived rollback")
	}
	if _, exists := state.vars.versions["new"]; exists {
		t.Fatal("new variable version survived rollback")
	}

	execution.statementDepth = 0
	execution.recordVariableRollback(state, "ignored")
	if len(execution.variableRollbacks) != 0 {
		t.Fatal("top-level rollback was recorded")
	}
}

func TestSuccessorPathIdentityAssignmentVariants(t *testing.T) {
	execution, parent := newNoOpExecutor(context.Background(), 20, &Request{})
	execution.assignSuccessorPaths(parent, 0, nil)

	single := &pathResult{state: parent.clone(), status: StatusCompleted}
	execution.assignSuccessorPaths(nil, 0, []*pathResult{single})
	if single.state.pathID == 0 {
		t.Fatal("single successor did not inherit a path ID")
	}

	first := parent.clone()
	first.pathID = 100
	second := parent.clone()
	second.pathID = 101
	execution.assignSuccessorPaths(parent, 0, []*pathResult{{state: first}, {state: second}})
	if first.pathID != 100 || second.pathID != 101 {
		t.Fatal("already distinct successor paths were reassigned")
	}

	duplicateID := uint64(200)
	first = parent.clone()
	first.pathID = duplicateID
	second = parent.clone()
	second.pathID = duplicateID
	execution.assignSuccessorPaths(parent, 0, []*pathResult{{state: first}, {state: second}})
	if first.pathID == second.pathID || first.parent == nil || second.parent == nil || first.parent != second.parent || !first.parent.frozen {
		t.Fatalf("forked successors = first %#v, second %#v", first, second)
	}
}

func TestResolveExitStatusKnownUnknownAndLimited(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 20, &Request{})
	known := &pathResult{state: state, status: StatusCompleted}
	paths, err := execution.resolveExitStatus(known, unknownLocation)
	if err != nil || len(paths) != 1 || paths[0] != known {
		t.Fatalf("known exit status = %#v, %v", paths, err)
	}
	unknown := &pathResult{state: state.clone(), status: StatusCompleted}
	unknown.state.exitStatus = newUnresolved(0)
	paths, err = execution.resolveExitStatus(unknown, unknownLocation)
	if err != nil || len(paths) != 2 {
		t.Fatalf("unknown exit status = %#v, %v", paths, err)
	}
	first, _ := paths[0].state.exitStatus.Data()
	second, _ := paths[1].state.exitStatus.Data()
	if first != 0 || second != 1 || paths[0].state.pathID == paths[1].state.pathID {
		t.Fatalf("resolved exit paths = %#v", paths)
	}

	limitedExecution, limitedState := newNoOpExecutor(context.Background(), 0, &Request{})
	limitedExecution.config.MaxExecutionSteps = 1
	limitedExecution.executedSteps = 1
	limitedState.exitStatus = newUnresolved(0)
	limited := &pathResult{state: limitedState, status: StatusCompleted}
	paths, err = limitedExecution.resolveExitStatus(limited, unknownLocation)
	if err != nil || len(paths) != 1 || paths[0].status != StatusIncomplete {
		t.Fatalf("limited exit resolution = %#v, %v", paths, err)
	}
}
