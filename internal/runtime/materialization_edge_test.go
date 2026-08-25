package runtime

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func TestWordMaterializationPreflightShapes(t *testing.T) {
	literal := &syntax.Lit{Value: "literal"}
	if wordNeedsMaterializationPreflight(nil) || wordNeedsMaterializationPreflight(&syntax.Word{}) {
		t.Fatal("nil and empty words must not require a preflight")
	}
	if wordPartsNeedMaterializationPreflight([]syntax.WordPart{literal}) {
		t.Fatal("one literal unexpectedly requires a preflight")
	}
	if !wordPartsNeedMaterializationPreflight([]syntax.WordPart{literal, literal}) {
		t.Fatal("a multipart word must require a preflight")
	}
	quoted := &syntax.DblQuoted{Parts: []syntax.WordPart{literal, literal}}
	if !wordPartsNeedMaterializationPreflight([]syntax.WordPart{quoted}) {
		t.Fatal("a multipart quoted word must require a preflight")
	}
	parameter := &syntax.ParamExp{Param: &syntax.Lit{Value: "value"}, Repl: &syntax.Replace{Orig: &syntax.Word{Parts: []syntax.WordPart{literal}}}}
	if !wordPartsNeedMaterializationPreflight([]syntax.WordPart{parameter}) {
		t.Fatal("parameter replacement must require a preflight")
	}
}

func TestMaterializationHelpersRespectCancellationAndLimits(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	execution, state := newNoOpExecutor(cancelled, 10, &Request{})
	word := &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: "value"}}}
	if _, _, err := execution.expandFieldsWithinLimit(execution.expansionConfig(state), word, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("expandFieldsWithinLimit() error = %v, want context.Canceled", err)
	}

	execution, state = newNoOpExecutor(context.Background(), 10, &Request{})
	execution.config.MaxMemoryBytes = 32
	if _, err := execution.addMaterializedString(31, "x"); err == nil {
		t.Fatal("oversized materialized string was accepted")
	}
	if err := execution.checkWordMaterialization(state, nil, 0); err != nil {
		t.Fatalf("nil word preflight: %v", err)
	}
	if _, _, err := execution.expandFieldsWithinLimit(execution.expansionConfig(state), word, 32); err == nil {
		t.Fatal("field expansion above the limit was accepted")
	}
}

func TestVariableMaterializationKinds(t *testing.T) {
	execution := &ExecutionContext{ctx: context.Background(), config: Config{MaxMemoryBytes: 64}}
	tests := []*struct {
		name    string
		value   *expand.Variable
		wantErr bool
	}{
		{name: "small string", value: &expand.Variable{Kind: expand.String, Str: "ok"}},
		{name: "large string", value: &expand.Variable{Kind: expand.String, Str: strings.Repeat("x", 65)}, wantErr: true},
		{name: "small indexed", value: &expand.Variable{Kind: expand.Indexed, List: []string{"a"}}},
		{name: "large indexed", value: &expand.Variable{Kind: expand.Indexed, List: []string{strings.Repeat("x", 64)}}, wantErr: true},
		{name: "small associative", value: &expand.Variable{Kind: expand.Associative, Map: map[string]string{"a": "b"}}},
		{name: "large associative", value: &expand.Variable{Kind: expand.Associative, Map: map[string]string{strings.Repeat("k", 40): strings.Repeat("v", 40)}}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := execution.checkVariableMaterialization(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("checkVariableMaterialization() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestStateAndRetainedBudgetAccountingEdges(t *testing.T) {
	if total, ok := stateMaterialization(nil); total != 0 || !ok {
		t.Fatalf("nil state materialization = %d, %v", total, ok)
	}
	execution, state := newNoOpExecutor(context.Background(), 10, &Request{})
	state.user = "user"
	state.commandStates["command"] = []uint64{1, 2}
	state.loopExitStatuses = []*uncertain[int]{newCertain(0)}
	state.issue = errors.New("issue")
	state.stdin = newCertain([]byte("in"))
	state.stdout = newCertain([]byte("out"))
	state.stderr = newCertain([]byte("err"))
	state.pipelineStatuses = newCertain([]string{"0", "1"})
	if total, ok := stateMaterialization(state); !ok || total <= 0 {
		t.Fatalf("populated state materialization = %d, %v", total, ok)
	}

	overflow := newState(&Request{}, defaultMaxMemoryBytes)
	overflow.retainedParentBytes = math.MaxInt
	overflow.user = "x"
	if _, ok := stateMaterialization(overflow); ok {
		t.Fatal("overflowing state materialization was accepted")
	}

	memory := execution.traceMemoryWithAggregate(state, 123, 2)
	if memory.AggregateBytes != 123 || memory.RetainedPaths != 2 || memory.StreamBytes != len("inouterr") {
		t.Fatalf("trace memory = %#v", memory)
	}
	if total, paths, auxiliary := execution.traceAggregateMemory(nil); total != 0 || paths != 0 || auxiliary != 0 {
		t.Fatalf("nil aggregate = %d, %d, %d", total, paths, auxiliary)
	}
	if total, _, _ := execution.traceAggregateMemory(&retainedPathBudget{pathCount: math.MaxInt}); total != math.MaxInt {
		t.Fatalf("overflow aggregate = %d, want MaxInt", total)
	}
}

func TestRetainedPathBudgetReplacementAndFailures(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 10, &Request{})
	completed := &pathResult{state: state, status: StatusCompleted}
	budget, err := execution.newRetainedPathBudget([]*pathResult{nil, completed})
	if err != nil || budget.pathCount != 1 || !budget.baselineSet {
		t.Fatalf("newRetainedPathBudget() = %#v, %v", budget, err)
	}
	previousBytes, _, ok := retainedPathStateBytes(completed)
	if !ok {
		t.Fatal("completed path materialization overflowed")
	}
	successor := &pathResult{state: state.clone(), status: StatusUnresolved}
	if !budget.replace(previousBytes, true, []*pathResult{successor}) || budget.pathCount != 1 {
		t.Fatalf("replace() budget = %#v", budget)
	}
	if (&retainedPathBudget{}).replace(1, true, nil) {
		t.Fatal("invalid retained replacement was accepted")
	}
	if _, retained, ok := retainedPathStateBytes(nil); retained || !ok {
		t.Fatalf("nil retained path = retained %v, ok %v", retained, ok)
	}

	incomplete := &pathResult{state: state.clone(), status: StatusIncomplete}
	errExpected := errors.New("failure")
	if got := execution.failPaths([]*pathResult{nil, incomplete, completed}, errExpected, unknownLocation); !errors.Is(got, errExpected) {
		t.Fatalf("failPaths() = %v", got)
	}
	if completed.status != StatusIncomplete || !errors.Is(completed.state.issue, errExpected) {
		t.Fatalf("completed path was not failed: %#v", completed)
	}
	if incomplete.state.issue != nil {
		t.Fatal("existing incomplete path was modified")
	}
}

func TestRepeatedPathAndAuxiliaryMaterializationEdges(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 10, &Request{})
	path := &pathResult{state: state, status: StatusCompleted}
	if err := execution.checkRepeatedPathMaterialization(path, 1, unknownLocation); err != nil {
		t.Fatalf("single repeated path: %v", err)
	}
	execution.config.MaxMemoryBytes = 1
	if err := execution.checkRepeatedPathMaterialization(path, 2, unknownLocation); err == nil || path.status != StatusIncomplete {
		t.Fatalf("oversized repeated path = %v, status %v", err, path.status)
	}

	execution, _ = newNoOpExecutor(context.Background(), 10, &Request{})
	execution.variableRollbackBytes = math.MaxInt
	execution.nestedShellBytes = 1
	if _, ok := execution.retainedAuxiliaryBytes(math.MaxInt); ok {
		t.Fatal("overflowing auxiliary bytes were accepted")
	}
}

func TestCommandInvocationAndBraceMaterializationEdges(t *testing.T) {
	_, state := newNoOpExecutor(context.Background(), 10, &Request{Env: map[string]string{"EXPORTED": "value"}})
	state.vars.putUnknown("EXPORTED", state.vars.Get("EXPORTED"))
	total, ok := commandInvocationMaterialization(state, "command", []*Argument{{Kind: ArgumentString, Value: "arg"}}, []byte("input"))
	if !ok || total <= materialize.EntryBytes {
		t.Fatalf("command invocation materialization = %d, %v", total, ok)
	}

	if result, ok := braceExpansionMaterialization(nil, -1); ok || result != nil {
		t.Fatalf("negative brace maximum = %#v, %v", result, ok)
	}
	if result, ok := braceExpansionMaterialization(nil, 0); !ok || result.count != 0 {
		t.Fatalf("nil brace word = %#v, %v", result, ok)
	}
	if _, ok := multiplyMaterialization(-1, 1, 1); ok {
		t.Fatal("negative multiplication was accepted")
	}
	if _, ok := multiplyMaterialization(2, 2, 3); ok {
		t.Fatal("overflowing multiplication was accepted")
	}
	if got := extraLeadingZeros("0007"); got != 3 {
		t.Fatalf("extraLeadingZeros() = %d, want 3", got)
	}
	if got := extraLeadingZeros("000"); got != 0 {
		t.Fatalf("all-zero extraLeadingZeros() = %d, want 0", got)
	}
}

func TestBraceMaterializationHelperFailuresAndSequences(t *testing.T) {
	if _, ok := addBraceAlternatives(&braceMaterialization{count: 1}, &braceMaterialization{count: 1}, materialize.EntryBytes); ok {
		t.Fatal("brace alternative count above the entry budget succeeded")
	}
	if _, ok := addBraceAlternatives(&braceMaterialization{literalBytes: 40}, &braceMaterialization{literalBytes: 30}, 64); ok {
		t.Fatal("brace alternative bytes above the budget succeeded")
	}
	if _, ok := multiplyBraceParts(&braceMaterialization{count: 2}, &braceMaterialization{count: 2}, 3*materialize.EntryBytes); ok {
		t.Fatal("brace product count above the entry budget succeeded")
	}
	if _, ok := multiplyBraceParts(&braceMaterialization{count: 1, literalBytes: 65}, &braceMaterialization{count: 1}, 64); ok {
		t.Fatal("left brace product bytes above the budget succeeded")
	}
	if _, ok := multiplyBraceParts(&braceMaterialization{count: 1}, &braceMaterialization{count: 1, literalBytes: 65}, 64); ok {
		t.Fatal("right brace product bytes above the budget succeeded")
	}
	if _, ok := multiplyBraceParts(&braceMaterialization{count: 1, literalBytes: 40}, &braceMaterialization{count: 1, literalBytes: 30}, 64); ok {
		t.Fatal("combined brace product bytes above the budget succeeded")
	}

	word := func(value string) *syntax.Word {
		return &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: value}}}
	}
	sequence := func(values ...string) *syntax.BraceExp {
		elements := make([]*syntax.Word, len(values))
		for index, value := range values {
			elements[index] = word(value)
		}
		return &syntax.BraceExp{Sequence: true, Elems: elements}
	}
	for _, brace := range []*syntax.BraceExp{
		sequence("1"),
		sequence("", "2"),
		sequence("1", ""),
	} {
		if parsed, ok := parseBraceSequence(brace, 10); ok || parsed != nil {
			t.Fatalf("invalid brace sequence = %#v, %v", parsed, ok)
		}
	}
	parsed, ok := parseBraceSequence(sequence("a", "c"), 10)
	if !ok || !parsed.character || parsed.first != int('a') || parsed.count != 3 {
		t.Fatalf("character sequence = %#v, %v", parsed, ok)
	}
	materialized, ok := braceSequenceMaterialization(sequence("a", "c"), 128)
	if !ok || materialized.count != 3 || materialized.literalBytes != 3 {
		t.Fatalf("character sequence materialization = %#v, %v", materialized, ok)
	}
	parsed, ok = parseBraceSequence(sequence("5", "1", "-2"), 10)
	if !ok || parsed.step != -2 || parsed.count != 3 {
		t.Fatalf("descending sequence = %#v, %v", parsed, ok)
	}
	parsed, ok = parseBraceSequence(sequence("1", "5", "-2"), 10)
	if !ok || parsed.step != 1 {
		t.Fatalf("wrong-direction step handling = %#v, %v", parsed, ok)
	}
	if parsed, ok := parseBraceSequence(sequence("1", "100"), 2); ok || parsed != nil {
		t.Fatalf("oversized brace sequence = %#v, %v", parsed, ok)
	}
	if materialized, ok := braceSequenceMaterialization(sequence("1", "3"), 1); ok || materialized != nil {
		t.Fatalf("undersized brace materialization = %#v, %v", materialized, ok)
	}
}
