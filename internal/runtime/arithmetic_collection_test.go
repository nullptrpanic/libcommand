package runtime

import (
	"context"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func TestArithmeticArrayReferenceResolution(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 20, &Request{})
	state.vars.put("indexed", expand.Variable{Set: true, Kind: expand.Indexed, List: []string{"zero", "one", "two"}})
	state.vars.put("map", expand.Variable{Set: true, Kind: expand.Associative, Map: map[string]string{"key": "value"}})
	environment := &bashArithmeticEnvironment{
		stateEnvironment: &stateEnvironment{state: state, maximum: defaultMaxMemoryBytes},
		ExecutionContext: execution,
		references:       make(map[string]*collectionReference),
	}
	for _, test := range []*struct {
		name        string
		wantName    string
		wantIndex   int
		wantKey     string
		associative bool
		ok          bool
	}{
		{name: "indexed[2]", wantName: "indexed", wantIndex: 2, ok: true},
		{name: "indexed[1+1]", wantName: "indexed", wantIndex: 2, ok: true},
		{name: "map[key]", wantName: "map", wantKey: "key", associative: true, ok: true},
		{name: "plain"},
		{name: "[1]"},
		{name: "bad-name[1]"},
	} {
		reference, ok := environment.arrayReference(test.name)
		if ok != test.ok {
			t.Fatalf("arrayReference(%q) = %#v, %v", test.name, reference, ok)
		}
		if ok && (reference.name != test.wantName || reference.index != test.wantIndex || reference.key != test.wantKey || reference.associative != test.associative) {
			t.Fatalf("arrayReference(%q) = %#v", test.name, reference)
		}
	}
	first, _ := environment.arrayReference("indexed[2]")
	second, _ := environment.arrayReference("indexed[2]")
	if first != second {
		t.Fatal("cached array reference was rebuilt")
	}
	if _, ok := environment.arrayReference("indexed[-1]"); ok || environment.err == nil || !strings.Contains(environment.err.Error(), "invalid array index") {
		t.Fatalf("negative array reference = %v", environment.err)
	}
}

func TestArithmeticEnvironmentCollectionGetAndSet(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 20, &Request{})
	state.vars.put("indexed", expand.Variable{Set: true, Kind: expand.Indexed, List: []string{"10", "20"}})
	state.vars.put("literal", expand.Variable{Set: true, Kind: expand.String, Str: " 16#ff "})
	state.vars.put("word", expand.Variable{Set: true, Kind: expand.String, Str: "not-a-number"})
	environment := &bashArithmeticEnvironment{
		stateEnvironment: &stateEnvironment{state: state, maximum: defaultMaxMemoryBytes},
		ExecutionContext: execution,
		references:       make(map[string]*collectionReference),
	}
	if got := environment.Get("indexed[1]"); got.String() != "20" {
		t.Fatalf("indexed arithmetic get = %#v", got)
	}
	if got := environment.Get("literal"); got.String() != "255" {
		t.Fatalf("literal arithmetic get = %#v", got)
	}
	if got := environment.Get("word"); got.String() != "not-a-number" {
		t.Fatalf("word arithmetic get = %#v", got)
	}
	if err := environment.Set("indexed[1]", expand.Variable{Set: true, Kind: expand.String, Str: "30"}); err != nil {
		t.Fatal(err)
	}
	if got := state.vars.Get("indexed").List[1]; got != "30" {
		t.Fatalf("indexed arithmetic set = %q", got)
	}
	if err := environment.Set("scalar", expand.Variable{Set: true, Kind: expand.String, Str: "40"}); err != nil {
		t.Fatal(err)
	}
	if got := state.vars.Get("scalar").String(); got != "40" {
		t.Fatalf("scalar arithmetic set = %q", got)
	}

	state.vars.put("invalid", expand.Variable{Set: true, Kind: expand.String, Str: "2#2"})
	if got := environment.Get("invalid"); got.String() != "0" || environment.err == nil {
		t.Fatalf("invalid arithmetic literal = %#v, %v", got, environment.err)
	}
}

func TestArithmeticMutationNodeRecognition(t *testing.T) {
	word := coverageWord("value")
	if got := arithmeticMutationWord(&syntax.BinaryArithm{Op: syntax.Assgn, X: word}); got != word {
		t.Fatalf("assignment mutation word = %#v", got)
	}
	if got := arithmeticMutationWord(&syntax.BinaryArithm{Op: syntax.Add, X: word}); got != nil {
		t.Fatalf("addition mutation word = %#v", got)
	}
	if got := arithmeticMutationWord(&syntax.UnaryArithm{Op: syntax.Inc, X: word}); got != word {
		t.Fatalf("increment mutation word = %#v", got)
	}
	if got := arithmeticMutationWord(&syntax.UnaryArithm{Op: syntax.Not, X: word}); got != nil {
		t.Fatalf("not mutation word = %#v", got)
	}
	if got := arithmeticMutationWord(word); got != nil {
		t.Fatalf("plain word mutation = %#v", got)
	}

	parameter := &syntax.ParamExp{Param: &syntax.Lit{Value: "array"}, Index: coverageWord("1")}
	arrayWord := &syntax.Word{Parts: []syntax.WordPart{parameter}}
	if got := nakedArrayParameter(arrayWord); got != parameter {
		t.Fatalf("naked array parameter = %#v", got)
	}
	parsed := parseForTest(t, `echo ${array[1]}`, "array.sh")
	dollarParameter := parsed.Stmts[0].Cmd.(*syntax.CallExpr).Args[1].Parts[0].(*syntax.ParamExp)
	if got := nakedArrayParameter(&syntax.Word{Parts: []syntax.WordPart{dollarParameter}}); got != nil {
		t.Fatalf("dollar array parameter = %#v", got)
	}
	if nakedArrayParameter(nil) != nil || nakedArrayParameter(&syntax.Word{Parts: []syntax.WordPart{word.Parts[0], word.Parts[0]}}) != nil {
		t.Fatal("invalid naked array parameter was recognized")
	}
}

func TestSplitArrayReferenceName(t *testing.T) {
	name, index, ok := splitArrayReferenceName("array[index]")
	if !ok || name != "array" || index != "index" {
		t.Fatalf("split reference = %q, %q, %v", name, index, ok)
	}
	for _, value := range []string{"array", "[0]", "bad-name[0]", "array[0"} {
		if _, _, ok := splitArrayReferenceName(value); ok {
			t.Fatalf("invalid reference %q was accepted", value)
		}
	}
}
