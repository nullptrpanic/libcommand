package runtime

import (
	"context"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func TestBashRegularExpressionNormalization(t *testing.T) {
	if got := normalizeBashRegularExpression(`\d\w\_\-`); got != `dw_\-` {
		t.Fatalf("normalized regular expression = %q", got)
	}
	for _, value := range []byte{'0', '9', 'A', 'Z', 'a', 'z', '_'} {
		if !isASCIIWordByte(value) {
			t.Fatalf("ASCII word byte %q was rejected", value)
		}
	}
	for _, value := range []byte{'-', ' ', 0x80} {
		if isASCIIWordByte(value) {
			t.Fatalf("non-word byte %q was accepted", value)
		}
	}
}

func TestVirtualFileTestTruthMatrix(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 20, &Request{})
	if err := state.fs.write("/known", []byte("value"), false); err != nil {
		t.Fatal(err)
	}
	if err := state.fs.writeValue("/unknown", []byte("representative"), false, true); err != nil {
		t.Fatal(err)
	}
	if err := state.fs.ensureDir("/directory"); err != nil {
		t.Fatal(err)
	}
	tests := []*struct {
		name     string
		path     string
		operator syntax.UnTestOperator
		want     truthValue
	}{
		{name: "device exists", path: "/dev/null", operator: syntax.TsExists, want: truthTrue},
		{name: "device character", path: "/dev/null", operator: syntax.TsCharSp, want: truthTrue},
		{name: "device regular", path: "/dev/null", operator: syntax.TsRegFile, want: truthFalse},
		{name: "device unsupported", path: "/dev/null", operator: syntax.UnTestOperator(255), want: truthUnknown},
		{name: "file exists", path: "/known", operator: syntax.TsExists, want: truthTrue},
		{name: "file nonempty", path: "/known", operator: syntax.TsNoEmpty, want: truthTrue},
		{name: "file not directory", path: "/known", operator: syntax.TsDirect, want: truthFalse},
		{name: "file unknown size", path: "/unknown", operator: syntax.TsNoEmpty, want: truthUnknown},
		{name: "file unsupported", path: "/known", operator: syntax.UnTestOperator(255), want: truthUnknown},
		{name: "directory exists", path: "/directory", operator: syntax.TsExists, want: truthTrue},
		{name: "directory regular", path: "/directory", operator: syntax.TsRegFile, want: truthFalse},
		{name: "directory unsupported", path: "/directory", operator: syntax.UnTestOperator(255), want: truthUnknown},
		{name: "missing", path: "/missing", operator: syntax.TsExists, want: truthFalse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := execution.virtualFileTestTruth(state, test.operator, test.path)
			if err != nil || got != test.want {
				t.Fatalf("virtualFileTestTruth() = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestVariableSetAndArithmeticTestEdges(t *testing.T) {
	execution, state := newNoOpExecutor(context.Background(), 20, &Request{})
	state.vars.put("set", expand.Variable{Set: true, Kind: expand.String, Str: "value"})
	state.vars.putUnknown("unknown", expand.Variable{Set: true, Kind: expand.String, Str: "representative"})
	state.vars.putIndexedWithCertainty("array", expand.Variable{Set: true, Kind: expand.Indexed, List: []string{"zero", "one"}}, false, nil)
	for _, test := range []*struct {
		name string
		want truthValue
	}{
		{name: "set", want: truthTrue},
		{name: "missing", want: truthFalse},
		{name: "unknown", want: truthUnknown},
		{name: "array[1]", want: truthTrue},
		{name: "array[-1]", want: truthFalse},
	} {
		got, _ := execution.variableSetTruth(state, test.name)
		if got != test.want {
			t.Fatalf("variableSetTruth(%q) = %v, want %v", test.name, got, test.want)
		}
	}
	if value, unknown, err := execution.arithmeticTestValue(state, ""); err != nil || unknown || value != 0 {
		t.Fatalf("empty arithmetic test = %d, %v, %v", value, unknown, err)
	}
	if value, unknown, err := execution.arithmeticTestValue(state, "1+2"); err != nil || unknown || value != 3 {
		t.Fatalf("expression arithmetic test = %d, %v, %v", value, unknown, err)
	}
	if _, _, err := execution.arithmeticTestValue(state, "2#2"); err == nil {
		t.Fatal("invalid arithmetic test literal succeeded")
	}
	failure := &testFailure{exitCode: 2, message: "failure"}
	if failure.Error() != "failure" {
		t.Fatalf("test failure error = %q", failure.Error())
	}
}

func TestMutationDetectionHelpers(t *testing.T) {
	assignment := &syntax.BinaryArithm{Op: syntax.Assgn, X: coverageWord("value"), Y: coverageWord("1")}
	addition := &syntax.BinaryArithm{Op: syntax.Add, X: coverageWord("1"), Y: coverageWord("2")}
	if !arithmMayMutate(assignment) || arithmMayMutate(addition) {
		t.Fatal("arithmetic mutation classification mismatch")
	}
	if arithmeticNodeMayMutate(addition) || !arithmeticNodeMayMutate(&syntax.UnaryArithm{Op: syntax.Dec, X: coverageWord("value")}) {
		t.Fatal("arithmetic node mutation classification mismatch")
	}
	if wordMayMutateVariables(nil) || wordMayMutateVariables(coverageWord("literal")) {
		t.Fatal("plain word was classified as mutating")
	}
	parameterWord := parseForTest(t, `echo ${value:=default}`, "mutation.sh").Stmts[0].Cmd.(*syntax.CallExpr).Args[1]
	if !wordMayMutateVariables(parameterWord) {
		t.Fatal("assigning parameter expansion was not classified as mutating")
	}
	commandWord := parseForTest(t, `echo $(echo value)`, "mutation.sh").Stmts[0].Cmd.(*syntax.CallExpr).Args[1]
	if wordMayMutateVariables(commandWord) {
		t.Fatal("nested command was classified as a parent-variable mutation")
	}
	if got := strings.TrimSpace(normalizeBashRegularExpression(" value ")); got != "value" {
		t.Fatalf("ordinary regular expression = %q", got)
	}
}
