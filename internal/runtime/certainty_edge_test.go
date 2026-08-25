package runtime

import (
	"context"
	"slices"
	"testing"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func TestCertaintyHelperEdgeCases(t *testing.T) {
	state := newState(&Request{Args: []string{"script", "plain", "*.txt"}}, defaultMaxMemoryBytes)
	if wordCertainty(state, nil) != 0 || arithmeticCertainty(state, nil) != 0 {
		t.Fatal("nil syntax has certainty")
	}
	if name, tilde := leadingTildeName(&syntax.Word{}); name != "" || tilde {
		t.Fatalf("empty tilde = %q, %v", name, tilde)
	}
	if name, tilde := leadingTildeName(&syntax.Word{Parts: []syntax.WordPart{&syntax.DblQuoted{}}}); name != "" || tilde {
		t.Fatalf("quoted tilde = %q, %v", name, tilde)
	}
	if name, tilde := leadingTildeName(&syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: "~root/path"}}}); name != "root" || !tilde {
		t.Fatalf("named tilde = %q, %v", name, tilde)
	}
	if wordHasRelativeGlob(state, &syntax.Word{}) {
		t.Fatal("empty word has a relative glob")
	}
	if wordHasRelativeGlob(state, &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: "/tmp/*"}}}) {
		t.Fatal("absolute glob is relative")
	}

	file := parseForTest(t, `echo @(one|two)`, "certainty-edge.sh")
	extglob := file.Stmts[0].Cmd.(*syntax.CallExpr).Args[1]
	if !wordMayExpandGlob(state, extglob) {
		t.Fatal("extended glob was not detected")
	}

	file = parseForTest(t, `echo $(value)`, "certainty-edge.sh")
	substitutionWord := file.Stmts[0].Cmd.(*syntax.CallExpr).Args[1]
	substitution := firstCommandSubstitution(substitutionWord)
	state.pushSubstitutionFrame()
	state.setSubstitution(substitution, &substitutionResult{stdout: newCertain([]byte("*.go")), exitStatus: newCertain(0)})
	if !wordMayExpandGlob(state, substitutionWord) {
		t.Fatal("resolved substitution glob was not detected")
	}
	state.popSubstitutionFrame()

	if parameterMayExpandGlob(state, nil) || parameterMayExpandGlob(state, &syntax.ParamExp{}) {
		t.Fatal("nil parameter expands a glob")
	}
	state.vars.put("PATTERN", expand.Variable{Set: true, Kind: expand.String, Str: "*.go"})
	plainParameter := &syntax.ParamExp{Param: &syntax.Lit{Value: "PATTERN"}}
	if !parameterMayExpandGlob(state, plainParameter) {
		t.Fatal("scalar parameter glob was not detected")
	}
	modifiedParameter := &syntax.ParamExp{Param: &syntax.Lit{Value: "PATTERN"}, Excl: true}
	if parameterMayExpandGlob(state, modifiedParameter) {
		t.Fatal("modified parameter was treated as a plain glob")
	}
	positional := &syntax.ParamExp{Param: &syntax.Lit{Value: "@"}}
	if !parameterMayExpandGlob(state, positional) {
		t.Fatal("positional glob was not detected")
	}

	if (&unknownValueError{}).Error() != "value depends on unresolved command output" {
		t.Fatal("empty unknown-value error changed")
	}
	execution, _ := newNoOpExecutor(context.Background(), 10, &Request{})
	value, unresolved, err := execution.literalValueWithCertainty(state, nil)
	if value != "" || unresolved || err != nil {
		t.Fatalf("nil literal = %q, %v, %v", value, unresolved, err)
	}
	if parameterValueUnknown(state, nil) {
		t.Fatal("nil parameter is unresolved")
	}
	if unknown, variable := parameterBaseUnknown(state, nil); unknown || variable.IsSet() {
		t.Fatalf("nil parameter base = %v, %#v", unknown, variable)
	}
}

func TestCertaintyUnknownStateQueries(t *testing.T) {
	state := newState(&Request{}, defaultMaxMemoryBytes)
	state.vars.putUnknown("EXPORTED", expand.Variable{Set: true, Kind: expand.String, Str: "representative", Exported: true})
	state.vars.putUnknown("LOCAL", expand.Variable{Set: true, Kind: expand.String, Str: "representative"})
	state.vars.putUnknown("UNSET", expand.Variable{Kind: expand.String, Exported: true})
	if got := unknownExportedVariables(state); !slices.Equal(got, []string{"EXPORTED"}) {
		t.Fatalf("unknown exports = %#v", got)
	}

	known := &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: "known"}}}
	unknown := &syntax.Word{Parts: []syntax.WordPart{&syntax.ParamExp{Param: &syntax.Lit{Value: "LOCAL"}}}}
	if index, found := firstUnknownWord(state, []*syntax.Word{known, unknown}); index != 1 || !found {
		t.Fatalf("first unknown word = %d, %v", index, found)
	}
	if index, found := firstUnknownWord(state, []*syntax.Word{known}); index != 0 || found {
		t.Fatalf("known words = %d, %v", index, found)
	}

	if !hostVariableUnknown(state, "$") || !hostVariableUnknown(state, "RANDOM") || !hostVariableUnknown(state, "UID") {
		t.Fatal("host-maintained variables were treated as known")
	}
	state.vars.put("UID", expand.Variable{Set: true, Kind: expand.String, Str: "1000"})
	if hostVariableUnknown(state, "UID") || hostVariableUnknown(state, "ordinary") {
		t.Fatal("known variables were treated as host-dependent")
	}
	state.backgroundPIDSet = true
	if !hostVariableUnknown(state, "!") {
		t.Fatal("background PID was treated as known")
	}
}
