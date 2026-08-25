package runtime

import (
	"context"
	"reflect"
	"testing"

	"mvdan.cc/sh/v3/expand"
)

func TestCommandContextDiscoverySnapshotsAndStateStore(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 20, &Request{})
	command := &CommandContext{execution: execution, state: state, source: unknownLocation}
	if command.CandidateContext() {
		t.Fatal("ordinary command was marked as candidate discovery")
	}
	execution.discoveryDepth = 1
	if !command.CandidateContext() {
		t.Fatal("candidate command context was not reported")
	}

	state.pathID = 7
	parent := state.clone()
	parent.pathID = 6
	state.parent = parent
	state.retainedParentBytes = 123
	snapshot := command.Snapshot()
	state.vars.put("VALUE", expand.Variable{Set: true, Kind: expand.String, Str: "changed"})
	command.Restore(snapshot)
	if state.vars.Get("VALUE").IsSet() || state.pathID != 7 || state.parent != parent || state.retainedParentBytes != 123 || state.frozen {
		t.Fatalf("restored state = %#v", state)
	}

	state.commandStates = nil
	values := []uint64{1, 2}
	command.SetCommandState("command", values)
	values[0] = 9
	got := command.CommandState("command")
	got[0] = 8
	if !reflect.DeepEqual(command.CommandState("command"), []uint64{1, 2}) {
		t.Fatalf("command state shares storage: %#v", command.CommandState("command"))
	}
}

func TestCommandContextOptionsVariablesAndScopes(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 20, &Request{})
	command := &CommandContext{execution: execution, state: state, source: unknownLocation}
	options := []string{"allexport", "errexit", "errtrace", "noglob", "nounset", "pipefail", "verbose", "xtrace", "dotglob", "extglob", "globstar", "inherit_errexit", "nocaseglob", "nullglob", "command-string"}
	for _, name := range options {
		if !command.SetOption(name, true) {
			t.Fatalf("SetOption(%q) failed", name)
		}
		if enabled, known := command.Option(name); !known || !enabled {
			t.Fatalf("Option(%q) = %v, %v", name, enabled, known)
		}
	}
	if command.SetOption("missing", true) {
		t.Fatal("unknown option was accepted")
	}
	if enabled, known := command.Option("missing"); enabled || known {
		t.Fatalf("unknown option = %v, %v", enabled, known)
	}

	if command.SaveLocal("VALUE") {
		t.Fatal("local save without a scope succeeded")
	}
	state.pushLocalScope()
	if !command.SaveLocal("VALUE") {
		t.Fatal("local save with a scope failed")
	}
	if err := command.DefineVariable("bad-name", &expand.Variable{}, false, nil); err == nil {
		t.Fatal("invalid variable definition succeeded")
	}
	if err := command.DefineVariable("VALUE", &expand.Variable{Set: true, Kind: expand.String, Str: "value"}, true, nil); err != nil {
		t.Fatal(err)
	}
	if command.Variable("VALUE").String() != "value" || !command.VariableUnknown("VALUE") {
		t.Fatalf("defined variable = %#v, unknown=%v", command.Variable("VALUE"), command.VariableUnknown("VALUE"))
	}
	seen := false
	command.EachVariable(func(name string, value *expand.Variable) bool {
		if name == "VALUE" && value.String() == "value" {
			seen = true
		}
		return !seen
	})
	if !seen {
		t.Fatal("EachVariable did not expose VALUE")
	}
}

func TestCommandContextTrapCopiesAndLoopStatusFallback(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 20, &Request{})
	command := &CommandContext{execution: execution, state: state, source: unknownLocation}
	command.SetTrap("EXIT", "echo exit")
	traps := command.Traps()
	traps["EXIT"] = "changed"
	if command.Traps()["EXIT"] != "echo exit" || state.exitTrapInherited {
		t.Fatalf("trap copy = %#v, inherited=%v", command.Traps(), state.exitTrapInherited)
	}
	command.DeleteTrap("EXIT")
	if len(command.Traps()) != 0 {
		t.Fatalf("deleted traps = %#v", command.Traps())
	}

	state.pushLoop()
	state.setLoopExitStatus(7, true)
	if code, unresolved := state.currentLoopExitStatus(); code != 7 || !unresolved {
		t.Fatalf("loop exit status = %d, %v", code, unresolved)
	}
	state.popLoop()
	state.pipelineStatuses = newCertain[[]string](nil)
	state.exitStatus = newUnresolved(9)
	if values, unresolved := state.pipelineStatusValues(); !reflect.DeepEqual(values, []string{"9"}) || !unresolved {
		t.Fatalf("pipeline status fallback = %#v, %v", values, unresolved)
	}
}

func TestCommandResultAllUnresolved(t *testing.T) {
	result := &CommandResult{outputs: []*CommandOutput{{
		Stdout:   newUnresolved[[]byte](nil),
		Stderr:   newUnresolved[[]byte](nil),
		ExitCode: newUnresolved(0),
	}}}
	if !result.AllUnresolved() {
		t.Fatal("fully unresolved command result was not recognized")
	}
	result.outputs[0].ExitCode = newCertain(0)
	if result.AllUnresolved() {
		t.Fatal("partially unresolved command result was treated as fully unresolved")
	}
}
