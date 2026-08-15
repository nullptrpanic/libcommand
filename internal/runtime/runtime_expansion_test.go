package runtime

import (
	"bytes"
	"context"
	"errors"
	iofs "io/fs"
	"reflect"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func TestParseBashIntegerLiteralWrapsToSigned64Bits(t *testing.T) {
	tests := map[string]int64{
		"9223372036854775808":  -9223372036854775808,
		"18446744073709551615": -1,
		"18446744073709551616": 0,
		"16#ffffffffffffffff":  -1,
		"16#10000000000000000": 0,
	}
	for input, want := range tests {
		got, recognized, err := parseBashIntegerLiteral(input)
		if err != nil || !recognized || got != want {
			t.Fatalf("parseBashIntegerLiteral(%q) = %d, %t, %v, want %d, true, nil", input, got, recognized, err, want)
		}
	}
}

func TestNormalizeArithmeticExpansionLiterals(t *testing.T) {
	file := parseForTest(t, `command "$((9223372036854775808))"`, "arithmetic-literal.sh")
	word := file.Stmts[0].Cmd.(*syntax.CallExpr).Args[1]
	if err := normalizeWordArithmeticLiterals(word); err != nil {
		t.Fatal(err)
	}
	var literal string
	syntax.Walk(word, func(node syntax.Node) bool {
		if expression, ok := node.(*syntax.ArithmExp); ok {
			literal = expression.X.(*syntax.Word).Lit()
		}
		return true
	})
	if literal != "-9223372036854775808" {
		t.Fatalf("normalized literal = %q", literal)
	}
	e, s := newNoOpExecutor(context.Background(), 10, &Request{})
	fields, err := e.expandWords(s, []*syntax.Word{word})
	if err != nil || !reflect.DeepEqual(fields, []string{"-9223372036854775808"}) {
		t.Fatalf("expanded fields = %#v, %v", fields, err)
	}
}

func TestMaskInactiveParameterWordsDoesNotAllocateWithoutMasks(t *testing.T) {
	file := parseForTest(t, `literal`, "literal.sh")
	word := file.Stmts[0].Cmd.(*syntax.CallExpr).Args[0]
	s := newState(&Request{}, defaultMaxMemoryBytes)
	allocations := testing.AllocsPerRun(1000, func() {
		restore := maskInactiveParameterWords(s, word)
		restore()
	})
	if allocations != 0 {
		t.Fatalf("allocations = %v, want 0", allocations)
	}
}

func TestWordCertaintyDoesNotAllocateForLiteral(t *testing.T) {
	file := parseForTest(t, `literal`, "literal.sh")
	word := file.Stmts[0].Cmd.(*syntax.CallExpr).Args[0]
	s := newState(&Request{}, defaultMaxMemoryBytes)
	allocations := testing.AllocsPerRun(1000, func() {
		if certainty := wordCertainty(s, word); certainty != 0 {
			t.Fatalf("certainty = %d, want 0", certainty)
		}
	})
	if allocations != 0 {
		t.Fatalf("allocations = %v, want 0", allocations)
	}
}

func TestWordMayMutateVariables(t *testing.T) {
	tests := []struct {
		name   string
		word   string
		mutate bool
	}{
		{name: "literal", word: `literal`},
		{name: "parameter read", word: `$value`},
		{name: "parameter assignment", word: `${value:=default}`, mutate: true},
		{name: "arithmetic read", word: `$((value + 1))`},
		{name: "arithmetic assignment", word: `$((value += 1))`, mutate: true},
		{name: "arithmetic increment", word: `$((value++))`, mutate: true},
		{name: "array index increment", word: `${values[index++]}`, mutate: true},
		{name: "command substitution is isolated", word: `$(value=inner)`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file := parseForTest(t, "command "+test.word, "mutation.sh")
			word := file.Stmts[0].Cmd.(*syntax.CallExpr).Args[1]
			if got := wordMayMutateVariables(word); got != test.mutate {
				t.Fatalf("wordMayMutateVariables(%q) = %t, want %t", test.word, got, test.mutate)
			}
		})
	}
}

func TestExportedVariablesAllocatesOnlyResultMap(t *testing.T) {
	variables := newVariables(map[string]string{
		"A": "one",
		"B": "two",
		"C": "three",
		"D": "four",
	})
	var exported map[string]string
	allocations := testing.AllocsPerRun(1000, func() {
		exported = variables.exported()
	})
	if len(exported) != 7 {
		t.Fatalf("exported variables = %#v", exported)
	}
	if allocations > 2 {
		t.Fatalf("allocations = %v, want at most 2", allocations)
	}
}

func TestExpansionSiteSubstitutionContinuation(t *testing.T) {
	t.Run("case pattern does not replay earlier body", func(t *testing.T) {
		script := `case value in
value) lark-cli first ;;&
"$(if [[ $RANDOM ]]; then echo value; else echo other; fi)") lark-cli second ;;
esac`
		paths, calls, err := runBash(t, script, &Request{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		requirePathStatus(t, paths, StatusCompleted)
		got := make([]string, len(calls))
		for index := range calls {
			got[index] = calls[index].args[0]
		}
		if !reflect.DeepEqual(got, []string{"first", "second"}) {
			t.Fatalf("calls=%#v", calls)
		}
	})

	for _, test := range []struct {
		name   string
		script string
		want   int
	}{
		{"arithmetic condition", `for ((i=0; i<2 && $(probe); i++)); do :; done`, 3},
		{"arithmetic post", `for ((i=0; i<2; i+=$(probe))); do :; done`, 2},
		{"unknown arithmetic command", `((RANDOM + $(probe)))`, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			callbacks := 0
			paths, _, err := runBash(t, test.script, &Request{}, func(config *Config, _ *[]*dispatchedCommand) {
				config.LookupCommand = lookupAllCommands(func(_ context.Context, _ *State, invocation *Invocation) (*CommandResult, error) {
					if invocation.Name == "probe" {
						callbacks++
						return &CommandResult{Stdout: []byte("1\n")}, nil
					}
					return &CommandResult{}, nil
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			requirePathStatus(t, paths, StatusCompleted)
			if callbacks != test.want {
				t.Fatalf("callbacks=%d, want %d", callbacks, test.want)
			}
		})
	}

	for _, test := range []struct {
		name   string
		script string
	}{
		{name: "arithmetic substitution error propagates", script: `for ((i=0; $(probe); i++)); do :; done`},
		{name: "case pattern substitution error propagates", script: `case value in "$(probe)") :;; esac`},
	} {
		t.Run(test.name, func(t *testing.T) {
			sentinel := errors.New(test.name)
			paths, _, err := runBash(t, test.script, &Request{}, func(config *Config, _ *[]*dispatchedCommand) {
				config.LookupCommand = lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
					return nil, sentinel
				})
			})
			if !errors.Is(err, sentinel) || len(paths) == 0 || paths[0].status != StatusIncomplete {
				t.Fatalf("paths=%#v err=%v", paths, err)
			}
		})
	}
}

func TestExpansionCallbacksAndRareAssignmentShapes(t *testing.T) {
	t.Run("command and process substitution callbacks", func(t *testing.T) {
		e, s := newNoOpExecutor(context.Background(), 20, &Request{})
		file := parseForTest(t, `echo "$(echo generated)"`, "expansion.sh")
		substitution := firstCommandSubstitution(file.Stmts[0].Cmd)
		if substitution == nil {
			t.Fatal("missing parsed command substitution")
		}

		s.pushSubstitutionFrame()
		s.setSubstitution(substitution, &substitutionResult{stdout: newCertain([]byte("memoized")), exitStatus: newCertain(4)})
		var output bytes.Buffer
		if err := e.expansionConfig(s).CmdSubst(&output, substitution); err != nil || output.String() != "memoized" {
			t.Fatalf("memoized callback output=%q err=%v", output.String(), err)
		}
		writeErr := errors.New("write rejected")
		if err := e.expansionConfig(s).CmdSubst(&rejectingWriter{err: writeErr}, substitution); !errors.Is(err, writeErr) {
			t.Fatalf("memoized writer error=%v", err)
		}

		fresh := newState(&Request{}, defaultMaxMemoryBytes)
		fresh.pushSubstitutionFrame()
		output.Reset()
		if err := e.expansionConfig(fresh).CmdSubst(&output, substitution); err == nil {
			t.Fatal("uncached substitution did not request evaluation")
		} else if request, requested := requestedSubstitution(err); !requested || request.state != fresh || request.substitution != substitution {
			t.Fatalf("substitution request=%#v err=%v", request, err)
		}
		process := &syntax.ProcSubst{}
		if value, err := e.expansionConfig(s).ProcSubst(process); value != "" || err == nil {
			t.Fatalf("process substitution=%q, %v", value, err)
		} else if request, requested := requestedProcessSubstitution(err); !requested || request.state != s || request.substitution != process {
			t.Fatalf("process substitution request=%#v, %v", request, err)
		}
	})

	t.Run("assignment and array error shapes", func(t *testing.T) {
		e, s := newNoOpExecutor(context.Background(), 20, &Request{})
		if err := e.applyAssignments(s, []*syntax.Assign{{}}, expand.Unknown, false); err != nil {
			t.Fatalf("nameless assignment=%v", err)
		}
		if value, err := e.literalValue(s, nil); err != nil || value != "" {
			t.Fatalf("nil literal=%q, %v", value, err)
		}

		randomArithmetic := &syntax.BinaryArithm{Op: syntax.Add, X: coverageWord("RANDOM"), Y: coverageWord("1")}
		if _, err := e.associativeIndex(s, randomArithmetic); err == nil || !strings.Contains(err.Error(), "host runtime") {
			t.Fatalf("host associative index=%v", err)
		}
		numericArithmetic := &syntax.BinaryArithm{Op: syntax.Add, X: coverageWord("2"), Y: coverageWord("3")}
		if value, err := e.associativeIndex(s, numericArithmetic); err != nil || value != "5" {
			t.Fatalf("numeric associative index=%q, %v", value, err)
		}

		for _, test := range []struct {
			name  string
			kind  expand.ValueKind
			array *syntax.ArrayExpr
			match string
		}{
			{"associative index", expand.Associative, &syntax.ArrayExpr{Elems: []*syntax.ArrayElem{{Index: randomArithmetic, Value: coverageWord("value")}}}, "host runtime"},
			{"associative value", expand.Associative, &syntax.ArrayExpr{Elems: []*syntax.ArrayElem{{Index: coverageWord("key"), Value: coverageProcessWord()}}}, "process substitution"},
			{"indexed host index", expand.Indexed, &syntax.ArrayExpr{Elems: []*syntax.ArrayElem{{Index: randomArithmetic, Value: coverageWord("value")}}}, "host runtime"},
			{"indexed invalid index", expand.Indexed, &syntax.ArrayExpr{Elems: []*syntax.ArrayElem{{Index: &syntax.BinaryArithm{Op: syntax.Quo, X: coverageWord("1"), Y: coverageWord("0")}, Value: coverageWord("value")}}}, "invalid indexed"},
			{"indexed value", expand.Indexed, &syntax.ArrayExpr{Elems: []*syntax.ArrayElem{{Value: coverageProcessWord()}}}, "process substitution"},
		} {
			t.Run(test.name, func(t *testing.T) {
				if _, _, _, err := e.arrayValue(s, test.array, test.kind); err == nil || !strings.Contains(err.Error(), test.match) {
					t.Fatalf("array error=%v", err)
				}
			})
		}

		addition := expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: "new"}
		if got := appendVariable(expand.Variable{}, addition); got.String() != "new" || !got.Exported {
			t.Fatalf("append unset=%#v", got)
		}
		got := appendVariable(expand.Variable{Set: true, Kind: expand.String, Str: "old"}, expand.Variable{Set: true, Kind: expand.Associative, Map: map[string]string{"k": "v"}})
		if got.Kind != expand.Associative || got.Map["k"] != "v" {
			t.Fatalf("append map to scalar=%#v", got)
		}

	})
}

func TestStateAndTruthTableRareBranches(t *testing.T) {
	t.Run("state cloning defaults and early iteration", func(t *testing.T) {
		values := newVariables(map[string]string{"HOME": "/custom", "A": "one"})
		if got := values.Get("HOME").String(); got != "/custom" {
			t.Fatalf("HOME=%q", got)
		}
		if got := values.Get("HOME someone").String(); got != "/" {
			t.Fatalf("tilde lookup=%q", got)
		}
		iterations := 0
		values.Each(func(string, expand.Variable) bool {
			iterations++
			return false
		})
		if iterations != 1 {
			t.Fatalf("iterations=%d", iterations)
		}

		source := []map[string]*savedVariable{{
			"array": {exists: true, value: expand.Variable{Set: true, Kind: expand.Indexed, List: []string{"parent"}}},
		}}
		cloned := cloneLocalScopes(source)
		clonedValue := cloned[0]["array"]
		clonedValue.value.List[0] = "child"
		cloned[0]["array"] = clonedValue
		if got := cloned[0]["array"].value.List[0]; got != "child" {
			t.Fatalf("cloned local=%q", got)
		}
		if got := source[0]["array"].value.List[0]; got != "parent" {
			t.Fatalf("source local changed=%q", got)
		}
	})

	if invertTruth(truthTrue) != truthFalse || invertTruth(truthFalse) != truthTrue || orTruth(truthFalse, truthFalse) != truthFalse || numericTruth(syntax.TsMatch, 1, 1) != truthUnknown {
		t.Fatal("truth boundary results")
	}
}

func TestDirectExecutionCancellationStopAndMalformedAssignments(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e, s := newNoOpExecutor(ctx, 10, &Request{})
	paths, err := e.evaluateStatement(s, &syntax.Stmt{Cmd: &syntax.CallExpr{Args: []*syntax.Word{coverageWord("never")}}})
	if !errors.Is(err, context.Canceled) || paths[0].status != StatusIncomplete || s.issue == nil {
		t.Fatalf("canceled evaluateStatement=%#v state=%#v err=%v", paths, s, err)
	}

	e, s = newNoOpExecutor(context.Background(), 10, &Request{})
	e.stop = true
	paths, err = e.evaluateStatements([]*pathResult{{state: s, status: StatusCompleted}}, []*syntax.Stmt{{}})
	if err != nil || paths[0].status != StatusTerminated {
		t.Fatalf("stopped evaluateStatements=%#v err=%v", paths, err)
	}
	e.stop = false
	for _, statement := range []*syntax.Stmt{{Coprocess: true}, {Disown: true}} {
		paths, err := e.evaluateStatement(s, statement)
		if err != nil || paths[0].status != StatusUnresolved {
			t.Fatalf("unsupported statement=%#v paths=%#v err=%v", statement, paths, err)
		}
	}
	paths, err = e.evaluateStatement(s, &syntax.Stmt{})
	if err != nil || paths[0].status != StatusCompleted || len(paths[0].state.stdout.data) != 0 || len(paths[0].state.stderr.data) != 0 {
		t.Fatalf("empty statement=%#v err=%v", paths, err)
	}

	e, s = newNoOpExecutor(context.Background(), 0, &Request{})
	call := &syntax.CallExpr{Args: []*syntax.Word{coverageWord("never")}}
	paths, err = e.evaluateStatement(s, &syntax.Stmt{Cmd: call})
	if err != nil || paths[0].status != StatusIncomplete || s.issue == nil {
		t.Fatalf("budget call=%#v state=%#v err=%v", paths, s, err)
	}
	declaration := &syntax.DeclClause{Variant: &syntax.Lit{Value: "declare"}}
	paths, err = e.evaluateStatement(s, &syntax.Stmt{Cmd: declaration})
	if err != nil || paths[0].status != StatusIncomplete {
		t.Fatalf("budget declaration=%#v err=%v", paths, err)
	}

	e, s = newNoOpExecutor(context.Background(), 10, &Request{})
	badAssignment := &syntax.Assign{Name: &syntax.Lit{Value: "X"}, Value: coverageRandomWord()}
	paths, err = e.evaluateStatement(s, &syntax.Stmt{Cmd: &syntax.CallExpr{Assigns: []*syntax.Assign{badAssignment}}})
	if err != nil || paths[0].status != StatusUnresolved || s.vars.Get("X").IsSet() {
		t.Fatalf("assignment-only error=%#v vars=%#v err=%v", paths, s.vars.data, err)
	}
	s.vars.put("X", expand.Variable{Set: true, Kind: expand.String, Str: "outer"})
	paths, err = e.evaluateStatement(s, &syntax.Stmt{Cmd: &syntax.CallExpr{Assigns: []*syntax.Assign{badAssignment}, Args: []*syntax.Word{coverageWord("cmd")}}})
	if err != nil || paths[0].status != StatusUnresolved || s.vars.Get("X").String() != "outer" {
		t.Fatalf("prefix assignment error=%#v vars=%#v err=%v", paths, s.vars.data, err)
	}
	declaration = &syntax.DeclClause{Variant: &syntax.Lit{Value: "declare"}, Args: []*syntax.Assign{badAssignment}}
	if paths, err = e.evaluateStatement(s, &syntax.Stmt{Cmd: declaration}); err != nil || paths[0].status != StatusUnresolved || s.vars.Get("X").String() != "outer" {
		t.Fatalf("declaration assignment error=%#v vars=%#v err=%v", paths, s.vars.data, err)
	}
}

func TestDirectTestEvaluationAndExpressionErrors(t *testing.T) {
	t.Run("synthetic test expression errors", func(t *testing.T) {
		e, s := newExecutorForTest(context.Background(), 20, &Request{}, nil)
		processWord := coverageProcessWord()
		normalWord := coverageWord("value")
		emptyWord := coverageWord("")
		randomWord := coverageRandomWord()
		for _, test := range []struct {
			name       string
			expression syntax.TestExpr
			want       truthValue
			wantErr    bool
		}{
			{"word error", processWord, truthFalse, true},
			{"parenthesized", &syntax.ParenTest{X: normalWord}, truthTrue, false},
			{"unary non-word", &syntax.UnaryTest{Op: syntax.TsEmpStr, X: &syntax.ParenTest{X: normalWord}}, truthUnknown, false},
			{"unary expansion error", &syntax.UnaryTest{Op: syntax.TsEmpStr, X: processWord}, truthUnknown, true},
			{"logical left error", &syntax.BinaryTest{Op: syntax.AndTest, X: processWord, Y: normalWord}, truthUnknown, true},
			{"logical right error", &syntax.BinaryTest{Op: syntax.OrTest, X: emptyWord, Y: processWord}, truthUnknown, true},
			{"binary non-word", &syntax.BinaryTest{Op: syntax.TsBefore, X: &syntax.ParenTest{X: normalWord}, Y: normalWord}, truthUnknown, false},
			{"binary left error", &syntax.BinaryTest{Op: syntax.TsBefore, X: processWord, Y: normalWord}, truthUnknown, true},
			{"binary right unknown", &syntax.BinaryTest{Op: syntax.TsBefore, X: normalWord, Y: randomWord}, truthUnknown, false},
			{"numeric arithmetic", &syntax.BinaryTest{Op: syntax.TsEql, X: coverageWord("not-number"), Y: coverageWord("1")}, truthFalse, false},
			{"unknown expression", nil, truthUnknown, false},
		} {
			t.Run(test.name, func(t *testing.T) {
				value, err := e.testTruth(s, test.expression)
				if value != test.want {
					t.Fatalf("value=%v want %v", value, test.want)
				}
				if (err != nil) != test.wantErr {
					t.Fatalf("err=%v wantErr=%t", err, test.wantErr)
				}
			})
		}
	})
}

func TestIfAndLogicalTerminalAndErrorPropagation(t *testing.T) {
	t.Run("if condition context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		e, s := newExecutorForTest(ctx, 20, &Request{}, noOpDispatch)
		clause := parseForTest(t, `if condition; then body; fi`, "flow-errors.sh").Stmts[0].Cmd.(*syntax.IfClause)
		paths, err := e.evaluateIf(s, clause)
		if !errors.Is(err, context.Canceled) || len(paths) != 1 || paths[0].status != StatusIncomplete || paths[0].state.issue == nil {
			t.Fatalf("paths=%#v err=%v", paths, err)
		}
	})

	t.Run("if stop and unresolved condition", func(t *testing.T) {
		e, s := newExecutorForTest(context.Background(), 20, &Request{}, func(context.Context, *State, *Invocation) (*CommandResult, error) {
			t.Fatal("dispatch while ExecutionContext stopped")
			return nil, nil
		})
		e.stop = true
		clause := parseForTest(t, `if condition; then body; fi`, "flow-errors.sh").Stmts[0].Cmd.(*syntax.IfClause)
		paths, err := e.evaluateIf(s, clause)
		if err != nil || len(paths) != 1 || paths[0].status != StatusTerminated {
			t.Fatalf("stopped paths=%#v err=%v", paths, err)
		}

		e, s = newExecutorForTest(context.Background(), 20, &Request{}, noOpDispatch)
		clause = parseForTest(t, `if echo value >$RANDOM; then body; fi`, "flow-errors.sh").Stmts[0].Cmd.(*syntax.IfClause)
		paths, err = e.evaluateIf(s, clause)
		if err != nil || len(paths) != 1 || paths[0].status != StatusUnresolved || paths[0].state.issue == nil {
			t.Fatalf("unresolved paths=%#v err=%v", paths, err)
		}
	})

	t.Run("if branch context error", func(t *testing.T) {
		requireCancellationAfterDispatch(t, "condition", func(e *ExecutionContext, s *State) ([]*pathResult, error) {
			clause := parseForTest(t, `if condition; then body; fi`, "flow-errors.sh").Stmts[0].Cmd.(*syntax.IfClause)
			return e.evaluateIf(s, clause)
		})
	})

	t.Run("logical left context error stop and unresolved", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		e, s := newExecutorForTest(ctx, 20, &Request{}, noOpDispatch)
		command := parseForTest(t, `left && right`, "flow-errors.sh").Stmts[0].Cmd.(*syntax.BinaryCmd)
		paths, err := e.evaluateLogical(s, command)
		if !errors.Is(err, context.Canceled) || len(paths) != 1 || paths[0].status != StatusIncomplete {
			t.Fatalf("canceled paths=%#v err=%v", paths, err)
		}

		e, s = newExecutorForTest(context.Background(), 20, &Request{}, func(context.Context, *State, *Invocation) (*CommandResult, error) {
			return &CommandResult{Action: CommandStop}, nil
		})
		paths, err = e.evaluateLogical(s, command)
		if err != nil || len(paths) != 1 || paths[0].status != StatusTerminated || !e.stop {
			t.Fatalf("stopped paths=%#v err=%v", paths, err)
		}

		e, s = newExecutorForTest(context.Background(), 20, &Request{}, noOpDispatch)
		command = parseForTest(t, `echo value >$RANDOM && right`, "flow-errors.sh").Stmts[0].Cmd.(*syntax.BinaryCmd)
		paths, err = e.evaluateLogical(s, command)
		if err != nil || len(paths) != 1 || paths[0].status != StatusUnresolved {
			t.Fatalf("unresolved paths=%#v err=%v", paths, err)
		}
	})

	t.Run("logical right context error", func(t *testing.T) {
		requireCancellationAfterDispatch(t, "left", func(e *ExecutionContext, s *State) ([]*pathResult, error) {
			command := parseForTest(t, `left && right`, "flow-errors.sh").Stmts[0].Cmd.(*syntax.BinaryCmd)
			return e.evaluateLogical(s, command)
		})
	})
}

func TestSubstitutionStatusAndRedirectionErrors(t *testing.T) {
	t.Run("command substitution input context and status", func(t *testing.T) {
		e, s := newNoOpExecutor(context.Background(), 20, &Request{})
		s.fs.write("/input", []byte("contents\n"), false)
		input := parseForTest(t, `echo "$(< /input)"`, "substitution.sh")
		paths, err := e.evaluateStatement(s, input.Stmts[0])
		if err != nil || len(paths) != 1 || string(paths[0].state.stdout.data) != "contents\n" {
			t.Fatalf("input paths=%#v err=%v", paths, err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		e, s = newNoOpExecutor(ctx, 20, &Request{})
		canceled := parseForTest(t, `echo "$(never)"`, "substitution.sh")
		paths, err = e.evaluateStatement(s, canceled.Stmts[0])
		if !errors.Is(err, context.Canceled) || len(paths) != 1 || paths[0].status != StatusIncomplete {
			t.Fatalf("canceled paths=%#v err=%v", paths, err)
		}

		e, s = newNoOpExecutor(context.Background(), 20, &Request{})
		process := parseForTest(t, `echo "$(echo <(never))"`, "substitution.sh")
		paths, err = e.evaluateStatement(s, process.Stmts[0])
		if err != nil || len(paths) != 1 || paths[0].status != StatusCompleted {
			t.Fatalf("process substitution paths=%#v err=%v", paths, err)
		}
	})

	t.Run("input-only substitution shapes", func(t *testing.T) {
		e, s := newNoOpExecutor(context.Background(), 20, &Request{})
		for _, substitution := range []*syntax.CmdSubst{
			{Stmts: []*syntax.Stmt{{}, {}}},
			{Stmts: []*syntax.Stmt{{Cmd: &syntax.IfClause{}, Redirs: []*syntax.Redirect{{Op: syntax.RdrIn, Word: coverageWord("input")}}}}},
			{Stmts: []*syntax.Stmt{{Cmd: &syntax.CallExpr{Assigns: []*syntax.Assign{{Name: &syntax.Lit{Value: "X"}, Value: coverageWord("value")}}}, Redirs: []*syntax.Redirect{{Op: syntax.RdrIn, Word: coverageWord("input")}}}}},
		} {
			result, failures, ok, err := e.inputOnlySubstitution(s, substitution)
			if err != nil || ok || result != nil || failures != nil {
				t.Fatalf("substitution=%#v result=%#v ok=%t err=%v", substitution, result, ok, err)
			}
		}
	})

	t.Run("redirection descriptors and expansion errors", func(t *testing.T) {
		e, s := newNoOpExecutor(context.Background(), 20, &Request{})
		for _, test := range []struct {
			name     string
			redirect *syntax.Redirect
			match    string
		}{
			{"named fd", &syntax.Redirect{N: &syntax.Lit{Value: "named"}, Op: syntax.RdrOut, Word: coverageWord("out")}, "named file descriptor"},
			{"input fd", &syntax.Redirect{N: &syntax.Lit{Value: "1"}, Op: syntax.RdrIn, Word: coverageWord("in")}, "input file descriptor"},
			{"here string expansion", &syntax.Redirect{Op: syntax.WordHdoc, Word: coverageRandomWord()}, "redirection"},
			{"here document fd", &syntax.Redirect{N: &syntax.Lit{Value: "1"}, Op: syntax.Hdoc, Hdoc: coverageWord("value")}, "here-document file descriptor"},
			{"here document expansion", &syntax.Redirect{Op: syntax.Hdoc, Hdoc: coverageProcessWord()}, "here-document"},
			{"duplicate fd", &syntax.Redirect{N: &syntax.Lit{Value: "1"}, Op: syntax.DplOut, Word: coverageWord("3")}, "output descriptor duplication"},
			{"duplicate expansion", &syntax.Redirect{N: &syntax.Lit{Value: "2"}, Op: syntax.DplOut, Word: coverageRandomWord()}, "redirection"},
		} {
			t.Run(test.name, func(t *testing.T) {
				_, err := e.prepareRedirections(s, []*syntax.Redirect{test.redirect})
				if err == nil || !strings.Contains(err.Error(), test.match) {
					t.Fatalf("error=%v", err)
				}
			})
		}
	})
}

func wantArgs(t *testing.T, calls []*dispatchedCommand, want []string) {
	t.Helper()
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].args, want) {
		t.Fatalf("calls = %#v, want args %v", calls, want)
	}
}

func TestExpansionDirectoryCacheReusesSnapshot(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 20, &Request{})
	for _, name := range []string{"/a", "/b"} {
		if err := s.fs.write(name, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	cache := make(map[string][]iofs.DirEntry)
	config := e.expansionConfigWithDirectoryCache(s, nil, cache)
	first, err := config.ReadDir2("/")
	if err != nil {
		t.Fatal(err)
	}
	second, err := config.ReadDir2("/")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || len(second) != 2 || first[0] != second[0] {
		t.Fatalf("directory snapshots were not reused: first=%#v second=%#v", first, second)
	}

	if err := s.fs.write("/c", nil, false); err != nil {
		t.Fatal(err)
	}
	fresh := e.expansionConfigWithDirectoryCache(s, nil, make(map[string][]iofs.DirEntry))
	entries, err := fresh.ReadDir2("/")
	if err != nil || len(entries) != 3 {
		t.Fatalf("fresh directory snapshot = %#v, %v", entries, err)
	}
}
