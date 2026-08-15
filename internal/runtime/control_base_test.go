package runtime

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type cancelDuringCommandContext struct {
	calls int
}

func (*cancelDuringCommandContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*cancelDuringCommandContext) Done() <-chan struct{}       { return nil }
func (c *cancelDuringCommandContext) Err() error {
	c.calls++
	if c.calls >= 2 {
		return context.Canceled
	}
	return nil
}
func (*cancelDuringCommandContext) Value(any) any { return nil }

func TestStatusZeroValueIsCompleted(t *testing.T) {
	var status Status
	if status != StatusCompleted {
		t.Fatalf("zero Status = %d, want StatusCompleted", status)
	}
}

func TestMergeIssue(t *testing.T) {
	issue := errors.New("issue")
	parent := &State{}
	child := &State{issue: issue}
	mergeIssue(parent, child)
	if !errors.Is(parent.issue, issue) {
		t.Fatalf("merged state = %#v", parent)
	}
}

func TestExecuteTrustsInternalCommandLookup(t *testing.T) {
	file := parseForTest(t, `external`, "trusted-config.sh")
	if err := Execute(context.Background(), file, &Request{}, &Config{MaxExecutionSteps: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteChecksCancellationBeforeEvaluation(t *testing.T) {
	file := parseForTest(t, `external`, "canceled-state.sh")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Execute(ctx, file, &Request{}, &Config{
		MaxExecutionSteps: 1, LookupCommand: lookupAllCommands(noOpDispatch),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context.Canceled", err)
	}
}

func TestExecuteTreatsNilDispatchResultAsUnhandled(t *testing.T) {
	file := parseForTest(t, `external`, "nil-result.sh")
	err := Execute(context.Background(), file, &Request{}, &Config{
		MaxExecutionSteps: 1, LookupCommand: lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecutePreservesCancellationCauseFromExecutionStepCheck(t *testing.T) {
	file := parseForTest(t, `external`, "cancel.sh")
	ctx := &cancelDuringCommandContext{}
	err := Execute(ctx, file, &Request{}, &Config{
		MaxExecutionSteps: 1, LookupCommand: lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
			t.Fatal("dispatch after cancellation")
			return nil, nil
		}),
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context.Canceled", err)
	}
}

func TestExecutionStepReservationIsAtomicAndOverflowSafe(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 3, &Request{})
	if status := e.reserveExecutionSteps(s, 2, unknownLocation); status != StatusCompleted {
		t.Fatalf("first reservation status = %d", status)
	}
	if status := e.reserveExecutionSteps(s, 2, unknownLocation); status != StatusIncomplete {
		t.Fatalf("second reservation status = %d", status)
	}
	if e.executedSteps != 2 {
		t.Fatalf("executed steps = %d, want 2", e.executedSteps)
	}

	e, s = newNoOpExecutor(context.Background(), math.MaxInt, &Request{})
	e.executedSteps = math.MaxInt - 1
	if status := e.reserveExecutionSteps(s, 2, unknownLocation); status != StatusIncomplete {
		t.Fatalf("overflow reservation status = %d", status)
	}
	if e.executedSteps != math.MaxInt-1 {
		t.Fatalf("executed steps overflowed to %d", e.executedSteps)
	}
}

func TestAppendStreamsRejectsMaterializationWithoutMutation(t *testing.T) {
	e := &ExecutionContext{ctx: context.Background(), config: Config{MaxMemoryBytes: 8}}
	exact := newState(&Request{}, defaultMaxMemoryBytes)
	exact.stdout = newCertain([]byte("1234"))
	exact.stderr = newCertain([]byte("12"))
	if status := e.appendStreams(exact, []byte("34"), nil, false, false, unknownLocation); status != StatusCompleted {
		t.Fatalf("exact append status = %d, issue = %v", status, exact.issue)
	}

	s := newState(&Request{}, defaultMaxMemoryBytes)
	s.stdout = newCertain([]byte("1234"))
	s.stderr = newCertain([]byte("12"))

	status := e.appendStreams(s, []byte("34"), []byte("5"), false, false, unknownLocation)
	if status != StatusIncomplete {
		t.Fatalf("appendStreams() status = %d, want %d", status, StatusIncomplete)
	}
	if string(s.stdout.data) != "1234" || string(s.stderr.data) != "12" {
		t.Fatalf("streams changed after failure: stdout=%q stderr=%q", s.stdout.data, s.stderr.data)
	}
	if s.issue == nil || !strings.Contains(s.issue.Error(), "maximum materialized byte count 8 reached") {
		t.Fatalf("issue = %v", s.issue)
	}
}

func TestUnknownTwoWayForkConsumesExecutionStep(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
	}{
		{"test clause", `[[ $RANDOM ]]`},
		{"arithmetic", `((RANDOM))`},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := parseForTest(t, test.source, "fork.sh")
			paths, steps, err := evaluateForTest(context.Background(), file, &Request{}, &Config{
				MaxExecutionSteps: 1, LookupCommand: lookupAllCommands(noOpDispatch),
			})
			if err != nil {
				t.Fatal(err)
			}
			if steps != 1 || len(paths) != 1 || paths[0].status != StatusIncomplete {
				t.Fatalf("steps=%d paths=%#v", steps, paths)
			}
		})
	}
}

func TestObservedUnknownExitAndErrexitForkConsumeSteps(t *testing.T) {
	for _, test := range []struct {
		source string
		max    int
	}{
		{`unknown && :`, 2},
		{`set -e; unknown`, 2},
	} {
		paths, steps, err := evaluateForTest(context.Background(), parseForTest(t, test.source, "status.sh"), &Request{}, &Config{
			MaxExecutionSteps: test.max, LookupCommand: lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
				return nil, nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		if steps != test.max {
			t.Fatalf("source=%q steps=%d", test.source, steps)
		}
		foundIncomplete := false
		for _, path := range paths {
			foundIncomplete = foundIncomplete || path.status == StatusIncomplete
		}
		if !foundIncomplete {
			t.Fatalf("source=%q paths=%#v", test.source, paths)
		}
	}
}

func TestUnknownSelectReservesAllSuccessorsBeforeCloning(t *testing.T) {
	file := parseForTest(t, `select value in one two; do break; done`, "select.sh")
	e, s := newNoOpExecutor(context.Background(), 3, &Request{})
	s.stdin = newUnresolved[[]byte](nil)
	var err error
	e.candidates, err = buildCandidateIndex(context.Background(), file, commandCandidateLookup(e.config.LookupCommand))
	if err != nil {
		t.Fatal(err)
	}
	paths, err := e.evaluateStatements([]*pathResult{{state: s, status: StatusCompleted}}, file.Stmts)
	if err != nil {
		t.Fatal(err)
	}
	if e.executedSteps != 1 || len(paths) != 1 || paths[0].status != StatusIncomplete {
		t.Fatalf("steps=%d paths=%#v", e.executedSteps, paths)
	}
}

func TestUnknownArithmeticLoopReservesExitBranchBeforeCloning(t *testing.T) {
	file := parseForTest(t, `for ((; RANDOM; )); do break; done`, "for.sh")
	paths, steps, err := evaluateForTest(context.Background(), file, &Request{}, &Config{
		MaxExecutionSteps: 1, LookupCommand: lookupAllCommands(noOpDispatch),
	})
	if err != nil {
		t.Fatal(err)
	}
	if steps != 1 || len(paths) != 1 || paths[0].status != StatusIncomplete {
		t.Fatalf("steps=%d paths=%#v", steps, paths)
	}
}

func TestUnknownLoopReachesSharedExecutionStepBudget(t *testing.T) {
	file := parseForTest(t, `while [[ $RANDOM ]]; do lark-cli tick; done`, "loop.sh")

	callbacks := 0
	paths, executedSteps, err := evaluateForTest(context.Background(), file, &Request{}, &Config{
		MaxExecutionSteps: 16, LookupCommand: lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
			callbacks++
			return &CommandResult{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if executedSteps != 16 {
		t.Fatalf("executed steps = %d", executedSteps)
	}
	if callbacks != 5 || len(paths) != 6 {
		t.Fatalf("callbacks = %d, paths = %d", callbacks, len(paths))
	}
	for iteration := range paths[:5] {
		if path := paths[iteration]; path.status != StatusCompleted {
			t.Fatalf("exit path %d = %#v", iteration, path)
		}
	}
	if final := paths[5]; final.status != StatusIncomplete {
		t.Fatalf("continuation path = %#v", final)
	}
}

func TestUnknownCaseBranchesReachSharedExecutionStepBudget(t *testing.T) {
	file := parseForTest(t, `case "$RANDOM" in a);; b);; c);; d);; e);; esac`, "case.sh")
	paths, executedSteps, err := evaluateForTest(context.Background(), file, &Request{}, &Config{
		MaxExecutionSteps: 4, LookupCommand: lookupAllCommands(noOpDispatch),
	})
	if err != nil {
		t.Fatal(err)
	}
	if executedSteps != 4 {
		t.Fatalf("executed steps = %d, want 4", executedSteps)
	}
	foundIncomplete := false
	for _, path := range paths {
		foundIncomplete = foundIncomplete || path.status == StatusIncomplete
	}
	if !foundIncomplete {
		t.Fatalf("paths = %#v, want budget-limited path", paths)
	}
}

func TestCasePatternScanHonorsCancellation(t *testing.T) {
	ctx := &cancelDuringCommandContext{}
	e, s := newNoOpExecutor(ctx, 10, &Request{})
	item := &syntax.CaseItem{Patterns: []*syntax.Word{
		{Parts: []syntax.WordPart{&syntax.Lit{Value: "first"}}},
		{Parts: []syntax.WordPart{&syntax.Lit{Value: "second"}}},
	}}
	if _, err := e.caseItemTruth(s, "missing", false, item); !errors.Is(err, context.Canceled) {
		t.Fatalf("caseItemTruth() error = %v, want context.Canceled", err)
	}
	if ctx.calls < 2 {
		t.Fatalf("context checks = %d, want cancellation during pattern scanning", ctx.calls)
	}
}

func TestCasePatternScanConsumesExecutionBudget(t *testing.T) {
	file := parseForTest(t, `case value in first|second|third) :;; esac`, "case-budget.sh")
	paths, steps, err := evaluateForTest(context.Background(), file, &Request{}, &Config{
		MaxExecutionSteps: 2,
		MaxMemoryBytes:    defaultMaxMemoryBytes,
		LookupCommand:     lookupAllCommands(noOpDispatch),
	})
	if err != nil {
		t.Fatal(err)
	}
	if steps != 2 || len(paths) != 1 || paths[0].status != StatusIncomplete {
		t.Fatalf("steps = %d, paths = %#v; want exhausted case-pattern budget", steps, paths)
	}
}

func TestAssignmentOnlyLoopReachesExecutionStepBudget(t *testing.T) {
	file := parseForTest(t, `while x=1; do x=2; done`, "loop.sh")

	paths, executedSteps, err := evaluateForTest(context.Background(), file, &Request{}, &Config{
		MaxExecutionSteps: 32, LookupCommand: lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
			t.Fatal("assignment-only loop dispatched an external command")
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if executedSteps != 32 || len(paths) != 1 || paths[0].status != StatusIncomplete {
		t.Fatalf("paths = %#v, executed steps = %d", paths, executedSteps)
	}
}

func TestCommandSubstitutionRetryDoesNotDoubleCountOuterStatement(t *testing.T) {
	file := parseForTest(t, `outer "$(inner)"`, "substitution-budget.sh")
	var calls []string
	paths, executedSteps, err := evaluateForTest(context.Background(), file, &Request{}, &Config{
		MaxExecutionSteps: 2, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, invocation *Invocation) (*CommandResult, error) {
			calls = append(calls, invocation.Name)
			return &CommandResult{Stdout: []byte("value\n")}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if executedSteps != 2 || len(paths) != 1 || paths[0].status != StatusCompleted {
		t.Fatalf("paths=%#v executed steps=%d", paths, executedSteps)
	}
	if !reflect.DeepEqual(calls, []string{"inner", "outer"}) {
		t.Fatalf("calls=%#v", calls)
	}
}

func TestStateForkUsesCopyOnWrite(t *testing.T) {
	parent := newState(&Request{}, defaultMaxMemoryBytes)
	parent.vars.put("value", expand.Variable{Set: true, Kind: expand.String, Str: "parent"})
	parent.setFunction("f", &syntax.FuncDecl{})
	parent.pushLocalScope()
	parent.stdout = newCertain([]byte("stdout"))
	parent.stderr = newCertain([]byte("stderr"))
	parent.stdin = newCertain([]byte("stdin"))
	parent.fs.write("/parent", []byte("file"), false)
	substitution := &syntax.CmdSubst{}
	largeOutput := []byte(strings.Repeat("x", 1<<20))
	parent.pushSubstitutionFrame()
	parent.setSubstitution(substitution, &substitutionResult{stdout: newCertain(largeOutput), exitStatus: newCertain(7)})

	child := parent.clone()
	if &parent.stdout.data[0] != &child.stdout.data[0] || &parent.stderr.data[0] != &child.stderr.data[0] || &parent.stdin.data[0] != &child.stdin.data[0] {
		t.Fatal("stream buffers were copied eagerly")
	}
	parentSubstitution, parentHasSubstitution := parent.substitution(substitution)
	childSubstitution, childHasSubstitution := child.substitution(substitution)
	if !parentHasSubstitution || !childHasSubstitution || string(parentSubstitution.stdout.data) != string(childSubstitution.stdout.data) || childSubstitution.exitStatus.data != 7 {
		t.Fatal("substitution frame was not cloned")
	}

	child.vars.put("value", expand.Variable{Set: true, Kind: expand.String, Str: "child"})
	child.setFunction("g", &syntax.FuncDecl{})
	child.deleteFunction("f")
	child.saveLocal("child-local")
	child.setSubstitution(substitution, &substitutionResult{stdout: newCertain([]byte("child")), exitStatus: newCertain(3)})
	child.fs.write("/child", []byte("child"), false)
	child.stdout = newCertain(append(append([]byte(nil), child.stdout.data...), '!'))

	if got := parent.vars.Get("value").String(); got != "parent" {
		t.Fatalf("parent variable = %q", got)
	}
	if _, exists := parent.functions["f"]; !exists {
		t.Fatal("child function deletion changed parent")
	}
	if _, exists := parent.functions["g"]; exists {
		t.Fatal("child function declaration changed parent")
	}
	if _, exists := parent.localScopes[0]["child-local"]; exists {
		t.Fatal("child local scope changed parent")
	}
	if got, exists := parent.substitution(substitution); !exists || string(got.stdout.data) != string(largeOutput) || got.exitStatus.data != 7 {
		t.Fatal("child substitution frame changed parent")
	}
	if got, _ := parent.fs.readValue("/child"); got != nil {
		t.Fatalf("child virtual file leaked to parent: %q", got)
	}
	if got := string(parent.stdout.data); got != "stdout" {
		t.Fatalf("child stdout changed parent: %q", got)
	}
}

func TestArithmeticMutationDetection(t *testing.T) {
	file := parseForTest(t, `((x=RANDOM))`, "arith.sh")
	command := file.Stmts[0].Cmd.(*syntax.ArithmCmd)
	if !arithmMayMutate(command.X) {
		t.Fatalf("mutation not detected in %T: %#v", command.X, command.X)
	}
}

func TestStateEnvironmentSpecialExitStatus(t *testing.T) {
	s := newState(&Request{Env: map[string]string{"EXPORTED": "value"}}, defaultMaxMemoryBytes)
	s.setExitCode(7)
	environment := &stateEnvironment{state: s}
	if got := environment.Get("?").String(); got != "7" {
		t.Fatalf("$? = %q", got)
	}
	var names []string
	environment.Each(func(name string, _ expand.Variable) bool {
		names = append(names, name)
		return true
	})
	if !slices.Contains(names, "EXPORTED") {
		t.Fatalf("environment names = %#v", names)
	}
	if err := environment.Set("?", expand.Variable{Set: true, Kind: expand.String, Str: "0"}); err == nil {
		t.Fatal("setting $? succeeded")
	}
	if err := environment.Set("NEW", expand.Variable{Set: true, Kind: expand.String, Str: "new"}); err != nil || s.vars.Get("NEW").String() != "new" {
		t.Fatalf("set NEW: value=%q error=%v", s.vars.Get("NEW").String(), err)
	}
}

func TestVariableCertaintyCopyOnWrite(t *testing.T) {
	s := newState(&Request{}, defaultMaxMemoryBytes)
	s.vars.putUnknown("value", expand.Variable{Set: true, Kind: expand.String})
	clone := s.clone()
	clone.vars.put("value", expand.Variable{Set: true, Kind: expand.String, Str: "known"})

	if !s.vars.isUnknown("value") || s.vars.Get("value").String() != "" {
		t.Fatalf("source variable = %#v unknown=%t", s.vars.Get("value"), s.vars.isUnknown("value"))
	}
	if clone.vars.isUnknown("value") || clone.vars.Get("value").String() != "known" {
		t.Fatalf("clone variable = %#v unknown=%t", clone.vars.Get("value"), clone.vars.isUnknown("value"))
	}
	clone.vars.delete("value")
	if clone.vars.isUnknown("value") {
		t.Fatal("deleted variable retained certainty")
	}
}

func TestUnknownLocalScopeRestoration(t *testing.T) {
	s := newState(&Request{}, defaultMaxMemoryBytes)
	s.vars.putUnknown("value", expand.Variable{Set: true, Kind: expand.String})
	s.pushLocalScope()
	s.saveLocal("value")
	s.vars.put("value", expand.Variable{Set: true, Kind: expand.String, Str: "local"})
	s.popLocalScope()
	if !s.vars.isUnknown("value") || s.vars.Get("value").String() != "" {
		t.Fatalf("restored variable = %#v unknown=%t", s.vars.Get("value"), s.vars.isUnknown("value"))
	}

	s.pushLocalScope()
	s.saveLocal("missing")
	s.vars.putUnknown("missing", expand.Variable{Set: true, Kind: expand.String})
	s.popLocalScope()
	if s.vars.Get("missing").IsSet() || s.vars.isUnknown("missing") {
		t.Fatalf("missing local survived: %#v", s.vars.Get("missing"))
	}
}

func TestEvaluateSourceTextHonorsCancellationBeforeParsing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e, s := newNoOpExecutor(ctx, 10, &Request{})
	paths, err := e.evaluateSourceText(s, "", "eval")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("evaluateSourceText() error = %v, want context.Canceled; paths = %#v", err, paths)
	}
}
