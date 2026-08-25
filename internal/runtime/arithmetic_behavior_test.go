package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func TestBashArithmeticBinaryOperators(t *testing.T) {
	tests := []struct {
		name     string
		operator syntax.BinAritOperator
		left     int64
		right    int64
		want     int64
		wantErr  string
	}{
		{name: "add", operator: syntax.Add, left: 7, right: 3, want: 10},
		{name: "subtract", operator: syntax.Sub, left: 7, right: 3, want: 4},
		{name: "multiply", operator: syntax.Mul, left: 7, right: 3, want: 21},
		{name: "divide", operator: syntax.Quo, left: 7, right: 3, want: 2},
		{name: "divide by zero", operator: syntax.Quo, left: 7, wantErr: "division by zero"},
		{name: "remainder", operator: syntax.Rem, left: 7, right: 3, want: 1},
		{name: "remainder by zero", operator: syntax.Rem, left: 7, wantErr: "division by zero"},
		{name: "power", operator: syntax.Pow, left: 3, right: 4, want: 81},
		{name: "zero power", operator: syntax.Pow, left: 3, want: 1},
		{name: "negative power", operator: syntax.Pow, left: 3, right: -1, wantErr: "exponent less than 0"},
		{name: "equal true", operator: syntax.Eql, left: 3, right: 3, want: 1},
		{name: "equal false", operator: syntax.Eql, left: 3, right: 4},
		{name: "greater", operator: syntax.Gtr, left: 4, right: 3, want: 1},
		{name: "less", operator: syntax.Lss, left: 3, right: 4, want: 1},
		{name: "not equal", operator: syntax.Neq, left: 3, right: 4, want: 1},
		{name: "less equal", operator: syntax.Leq, left: 4, right: 4, want: 1},
		{name: "greater equal", operator: syntax.Geq, left: 4, right: 4, want: 1},
		{name: "bit and", operator: syntax.And, left: 6, right: 3, want: 2},
		{name: "bit or", operator: syntax.Or, left: 6, right: 3, want: 7},
		{name: "bit xor", operator: syntax.Xor, left: 6, right: 3, want: 5},
		{name: "shift right masks count", operator: syntax.Shr, left: 8, right: 65, want: 4},
		{name: "shift left masks count", operator: syntax.Shl, left: 3, right: 65, want: 6},
		{name: "logical and true", operator: syntax.AndArit, left: 1, right: 2, want: 1},
		{name: "logical and false", operator: syntax.AndArit, left: 1, right: 0},
		{name: "logical or true", operator: syntax.OrArit, left: 0, right: 2, want: 1},
		{name: "logical or false", operator: syntax.OrArit, left: 0, right: 0},
		{name: "logical xor true", operator: syntax.XorBool, left: 0, right: 2, want: 1},
		{name: "logical xor false", operator: syntax.XorBool, left: 2, right: 3},
		{name: "unsupported", operator: syntax.BinAritOperator(255), wantErr: "unsupported binary arithmetic operator"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := applyBashArithmeticOperator(test.operator, test.left, test.right)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("result = %d, error = %v, want %d", got, err, test.want)
			}
		})
	}
}

func TestArithmeticAssignmentOperatorMappings(t *testing.T) {
	tests := []struct {
		assignment syntax.BinAritOperator
		operator   syntax.BinAritOperator
	}{
		{syntax.AddAssgn, syntax.Add},
		{syntax.SubAssgn, syntax.Sub},
		{syntax.MulAssgn, syntax.Mul},
		{syntax.QuoAssgn, syntax.Quo},
		{syntax.RemAssgn, syntax.Rem},
		{syntax.AndAssgn, syntax.And},
		{syntax.OrAssgn, syntax.Or},
		{syntax.XorAssgn, syntax.Xor},
		{syntax.ShlAssgn, syntax.Shl},
		{syntax.ShrAssgn, syntax.Shr},
		{syntax.AndBoolAssgn, syntax.AndArit},
		{syntax.OrBoolAssgn, syntax.OrArit},
		{syntax.XorBoolAssgn, syntax.XorBool},
		{syntax.PowAssgn, syntax.Pow},
	}
	for _, test := range tests {
		got, ok := arithmeticAssignmentOperator(test.assignment)
		if !ok || got != test.operator {
			t.Fatalf("assignment %q maps to %q, %v; want %q, true", test.assignment, got, ok, test.operator)
		}
	}
	if _, ok := arithmeticAssignmentOperator(syntax.Assgn); ok {
		t.Fatal("plain assignment unexpectedly mapped to a binary operator")
	}
}

func TestBashIntegerLiteralForms(t *testing.T) {
	tests := []struct {
		value      string
		want       int64
		recognized bool
		wantErr    bool
	}{
		{value: "", recognized: false},
		{value: "+", recognized: false},
		{value: "word", recognized: false},
		{value: "42", want: 42, recognized: true},
		{value: "+42", want: 42, recognized: true},
		{value: "-42", want: -42, recognized: true},
		{value: "077", want: 63, recognized: true},
		{value: "0xff", want: 255, recognized: true},
		{value: "0X10", want: 16, recognized: true},
		{value: "2#1011", want: 11, recognized: true},
		{value: "36#Z", want: 35, recognized: true},
		{value: "64#_", want: 63, recognized: true},
		{value: "64#@", want: 62, recognized: true},
		{value: "1#1", recognized: true, wantErr: true},
		{value: "65#1", recognized: true, wantErr: true},
		{value: "2#", recognized: true, wantErr: true},
		{value: "0x", recognized: true, wantErr: true},
		{value: "08", recognized: true, wantErr: true},
		{value: "2#2", recognized: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got, recognized, err := parseBashIntegerLiteral(test.value)
			if recognized != test.recognized || (err != nil) != test.wantErr || (!test.wantErr && got != test.want) {
				t.Fatalf("parse %q = %d, %v, %v; want %d, %v, error=%v", test.value, got, recognized, err, test.want, test.recognized, test.wantErr)
			}
		})
	}
}

func TestArithmeticEvaluationMatchesBashControlSemantics(t *testing.T) {
	execution := &ExecutionContext{ctx: context.Background(), config: Config{MaxMemoryBytes: defaultMaxMemoryBytes}}
	state := newState(&Request{}, defaultMaxMemoryBytes)
	tests := []struct {
		expression string
		want       int
	}{
		{expression: "2 ? 7 : 9", want: 7},
		{expression: "0 ? 7 : 9", want: 9},
		{expression: "0 && (x=1)", want: 0},
		{expression: "1 || (x=2)", want: 1},
		{expression: "x=3, ++x", want: 4},
		{expression: "x++", want: 4},
		{expression: "--x", want: 4},
		{expression: "!0", want: 1},
		{expression: "~0", want: -1},
		{expression: "+5", want: 5},
		{expression: "-5", want: -5},
		{expression: "x += 3", want: 7},
		{expression: "x *= 2", want: 14},
	}
	for _, test := range tests {
		expression, err := ParseArithmetic(context.Background(), test.expression)
		if err != nil {
			t.Fatalf("parse %q: %v", test.expression, err)
		}
		got, err := execution.arithmeticValue(state, expression)
		if err != nil || got != test.want {
			t.Fatalf("evaluate %q = %d, %v; want %d", test.expression, got, err, test.want)
		}
	}
	if value := state.vars.Get("x").String(); value != "14" {
		t.Fatalf("x = %q, want 14", value)
	}
	if value := state.vars.Get("missing"); value.IsSet() {
		t.Fatalf("short-circuited assignment created missing variable: %#v", value)
	}
}

func TestArithmeticEvaluationReportsInvalidExpressions(t *testing.T) {
	execution := &ExecutionContext{ctx: context.Background(), config: Config{MaxMemoryBytes: defaultMaxMemoryBytes}}
	state := newState(&Request{}, defaultMaxMemoryBytes)
	if _, err := execution.arithmeticValue(state, nil); err == nil {
		t.Fatal("missing expression succeeded")
	}
	flags := &syntax.FlagsArithm{}
	environment := &bashArithmeticEnvironment{
		stateEnvironment: &stateEnvironment{state: state, maximum: defaultMaxMemoryBytes},
		ExecutionContext: execution,
		references:       make(map[string]*collectionReference),
	}
	evaluator := &bashArithmeticEvaluator{config: execution.expansionConfig(state), environment: environment, resolving: make(map[string]struct{})}
	if _, err := evaluator.evaluate(flags); err == nil {
		t.Fatal("unsupported flags expression succeeded")
	}
	if _, err := evaluator.evaluate(nil); err == nil {
		t.Fatal("unsupported nil expression succeeded")
	}
	invalidTarget := &syntax.BinaryArithm{Op: syntax.Assgn, X: &syntax.ParenArithm{X: coverageWord("x")}, Y: coverageWord("1")}
	if _, err := evaluator.evaluate(invalidTarget); err == nil {
		t.Fatal("non-variable assignment target succeeded")
	}
	badUnary := &syntax.UnaryArithm{Op: syntax.UnAritOperator(255), X: coverageWord("1")}
	if _, err := evaluator.evaluate(badUnary); err == nil {
		t.Fatal("unsupported unary operator succeeded")
	}

	state.vars.put("a", expand.Variable{Set: true, Kind: expand.String, Str: "b"})
	state.vars.put("b", expand.Variable{Set: true, Kind: expand.String, Str: "a"})
	if _, err := evaluator.evaluateText("a"); err == nil || !strings.Contains(err.Error(), "recursion") {
		t.Fatalf("recursive variables error = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	execution.ctx = cancelled
	if _, err := evaluator.evaluate(coverageWord("1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled evaluation error = %v", err)
	}
}
