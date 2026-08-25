package runtime

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func TestScalarArrayAssignmentVariants(t *testing.T) {
	if _, ok := scalarArrayAssignment(expand.Variable{}, expand.String, expand.Variable{Kind: expand.String}, false); ok {
		t.Fatal("scalar-to-scalar assignment was treated as an array assignment")
	}
	indexed, ok := scalarArrayAssignment(
		expand.Variable{Set: true, Kind: expand.String, Str: "old"},
		expand.Indexed,
		expand.Variable{Set: true, Kind: expand.String, Str: "new"},
		true,
	)
	if !ok || !reflect.DeepEqual(indexed.List, []string{"oldnew"}) {
		t.Fatalf("promoted indexed assignment = %#v, %v", indexed, ok)
	}
	indexed, ok = scalarArrayAssignment(indexed, expand.Indexed, expand.Variable{Kind: expand.String, Str: "replacement"}, false)
	if !ok || !reflect.DeepEqual(indexed.List, []string{"replacement"}) {
		t.Fatalf("indexed replacement = %#v, %v", indexed, ok)
	}
	associative, ok := scalarArrayAssignment(
		expand.Variable{Set: true, Kind: expand.String, Str: "old"},
		expand.Associative,
		expand.Variable{Kind: expand.String, Str: "new"},
		true,
	)
	if !ok || associative.Map["0"] != "oldnew" {
		t.Fatalf("promoted associative assignment = %#v, %v", associative, ok)
	}
	associative, ok = scalarArrayAssignment(associative, expand.Associative, expand.Variable{Kind: expand.String, Str: "replacement"}, false)
	if !ok || associative.Map["0"] != "replacement" {
		t.Fatalf("associative replacement = %#v, %v", associative, ok)
	}
}

func TestIndexedSlotCombinationAndExtension(t *testing.T) {
	if slots := setIndexedSlot(nil, 2, 1); slots != nil {
		t.Fatalf("dense slot update = %#v", slots)
	}
	slots := setIndexedSlot(nil, 1, 3)
	if !reflect.DeepEqual(slots, map[int]struct{}{0: {}, 3: {}}) {
		t.Fatalf("sparse slot update = %#v", slots)
	}
	current := expand.Variable{Kind: expand.Indexed, List: []string{"a", "b"}}
	addition := expand.Variable{Kind: expand.Indexed, List: []string{"c", "d"}}
	if got := appendIndexedSlots(expand.Variable{}, addition, nil, nil); got != nil {
		t.Fatalf("non-indexed append slots = %#v", got)
	}
	if got := appendIndexedSlots(current, addition, nil, nil); got != nil {
		t.Fatalf("dense append slots = %#v", got)
	}
	got := appendIndexedSlots(current, addition, map[int]struct{}{1: {}}, nil)
	if !reflect.DeepEqual(got, map[int]struct{}{1: {}, 2: {}, 3: {}}) {
		t.Fatalf("sparse current append slots = %#v", got)
	}
	got = appendIndexedSlots(current, addition, nil, map[int]struct{}{1: {}})
	if !reflect.DeepEqual(got, map[int]struct{}{0: {}, 1: {}, 3: {}}) {
		t.Fatalf("sparse addition append slots = %#v", got)
	}

	values := []string{"existing"}
	if extended, err := extendIndexedArray(values, 0, 64); err != nil || &extended[0] != &values[0] {
		t.Fatalf("existing extension = %#v, %v", extended, err)
	}
	if _, err := extendIndexedArray(nil, 3, 32); err == nil {
		t.Fatal("indexed extension above materialization limit succeeded")
	}
}

func TestScalarAndSparseSliceHelpers(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 20, &Request{})
	state.vars.put("text", expand.Variable{Set: true, Kind: expand.String, Str: "a你好z"})
	parameter := func(source string) *syntax.ParamExp {
		file := parseForTest(t, "echo "+source, "slice.sh")
		call := file.Stmts[0].Cmd.(*syntax.CallExpr)
		return call.Args[1].Parts[0].(*syntax.ParamExp)
	}
	value, err := execution.sliceScalarValue(state, state.vars.Get("text"), parameter(`${text:1:2}`).Slice)
	if err != nil || value.Str != "你好" || value.Kind != expand.String || value.List != nil || value.Map != nil {
		t.Fatalf("UTF-8 slice = %#v, %v", value, err)
	}
	value, err = execution.sliceScalarValue(state, state.vars.Get("text"), parameter(`${text: -1}`).Slice)
	if err != nil || value.Str != "z" {
		t.Fatalf("negative slice = %#v, %v", value, err)
	}
	if _, err := execution.sliceScalarValue(state, state.vars.Get("text"), parameter(`${text:2:-9}`).Slice); err == nil {
		t.Fatal("invalid negative substring length succeeded")
	}
	if runeByteOffset("你好", 0) != 0 || runeByteOffset("你好", 1) != len("你") || runeByteOffset("你好", 9) != len("你好") {
		t.Fatal("rune byte offset mismatch")
	}

	if got := indexedExpansionKeys(nil, 3); !reflect.DeepEqual(got, []string{"0", "1", "2"}) {
		t.Fatalf("dense keys = %#v", got)
	}
	if got := indexedExpansionKeys(map[int]struct{}{4: {}, 1: {}}, 5); !reflect.DeepEqual(got, []string{"1", "4"}) {
		t.Fatalf("sparse keys = %#v", got)
	}
	slots := map[int]struct{}{1: {}, 4: {}, 8: {}}
	indices, err := execution.sparseIndexedExpansionIndices(state, slots, parameter(`${text:4:2}`).Slice)
	if err != nil || !reflect.DeepEqual(indices, []int{4, 8}) {
		t.Fatalf("sparse slice = %#v, %v", indices, err)
	}
	indices, err = execution.sparseIndexedExpansionIndices(state, slots, parameter(`${text: -2}`).Slice)
	if err != nil || !reflect.DeepEqual(indices, []int{8}) {
		t.Fatalf("negative sparse slice = %#v, %v", indices, err)
	}
	if _, err := execution.sparseIndexedExpansionIndices(state, slots, parameter(`${text:1:-1}`).Slice); err == nil {
		t.Fatal("negative sparse slice length succeeded")
	}
}

func TestTraceVariableFormatting(t *testing.T) {
	variables := newVariables(nil)
	indexed := expand.Variable{Kind: expand.Indexed, List: []string{"zero", "", "two"}}
	variables.putIndexedWithCertainty("indexed", indexed, false, map[int]struct{}{0: {}, 2: {}})
	if got := traceVariableKind(expand.Indexed); got != "indexed" {
		t.Fatalf("indexed kind = %q", got)
	}
	if got := traceVariableKind(expand.Associative); got != "associative" {
		t.Fatalf("associative kind = %q", got)
	}
	if got := traceVariableKind(expand.String); got != "string" {
		t.Fatalf("string kind = %q", got)
	}
	if got := traceVariableValue(variables, "indexed", &indexed); got != "0=zero\n2=two" {
		t.Fatalf("indexed trace value = %q", got)
	}
	associative := expand.Variable{Kind: expand.Associative, Map: map[string]string{"z": "last", "a": "first"}}
	if got := traceVariableValue(variables, "map", &associative); got != "a=first\nz=last" {
		t.Fatalf("associative trace value = %q", got)
	}
	scalar := expand.Variable{Kind: expand.String, Str: "value"}
	if got := traceVariableValue(variables, "scalar", &scalar); got != "value" {
		t.Fatalf("scalar trace value = %q", got)
	}
	if traceVariableValueBytes(variables, "indexed", &indexed) != len("0=zero\n2=two\n") || traceVariableValueBytes(variables, "map", &associative) != len("a=first\nz=last\n") {
		t.Fatal("trace variable byte accounting mismatch")
	}
}

func TestTraceStatementClassificationAndSnippet(t *testing.T) {
	tests := []*struct {
		command syntax.Command
		want    string
	}{
		{command: &syntax.CallExpr{}, want: "command"},
		{command: &syntax.IfClause{}, want: "condition"},
		{command: &syntax.CaseClause{}, want: "condition"},
		{command: &syntax.TestClause{}, want: "condition"},
		{command: &syntax.ForClause{}, want: "loop"},
		{command: &syntax.WhileClause{}, want: "loop"},
		{command: &syntax.BinaryCmd{}, want: "operator"},
		{command: &syntax.Subshell{}, want: "subshell"},
		{command: &syntax.Block{}, want: "block"},
		{command: &syntax.FuncDecl{}, want: "function"},
		{command: &syntax.ArithmCmd{}, want: "arithmetic"},
		{command: &syntax.DeclClause{}, want: "declaration"},
		{command: &syntax.LetClause{}, want: "arithmetic"},
		{command: nil, want: "statement"},
	}
	if got := traceStatementKind(nil); got != "statement" {
		t.Fatalf("nil statement kind = %q", got)
	}
	for _, test := range tests {
		if got := traceStatementKind(&syntax.Stmt{Cmd: test.command}); got != test.want {
			t.Fatalf("statement %T kind = %q, want %q", test.command, got, test.want)
		}
	}
	if got := traceSnippet("", &syntax.Stmt{Cmd: &syntax.Block{}}); got != "block" {
		t.Fatalf("invalid-position snippet = %q", got)
	}
	long := "echo " + strings.Repeat("你", 80)
	statement := parseForTest(t, long, "trace.sh").Stmts[0]
	if got := traceSnippet(long, statement); len(got) > 164 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long snippet = %q (%d bytes)", got, len(got))
	}
	noopRestore()
}
