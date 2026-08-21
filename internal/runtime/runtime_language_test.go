package runtime

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type dispatchedCommand struct {
	name  string
	args  []string
	stdin string
	dir   string
	env   map[string]string
}

type rejectingWriter struct {
	err error
}

func (w *rejectingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func coverageWord(value string) *syntax.Word {
	return &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: value}}}
}

func coverageProcessWord() *syntax.Word {
	return &syntax.Word{Parts: []syntax.WordPart{&syntax.ProcSubst{}}}
}

func coverageRandomWord() *syntax.Word {
	return &syntax.Word{Parts: []syntax.WordPart{&syntax.ParamExp{Short: true, Param: &syntax.Lit{Value: "RANDOM"}}}}
}

func firstCommandSubstitution(node syntax.Node) *syntax.CmdSubst {
	var result *syntax.CmdSubst
	syntax.Walk(node, func(current syntax.Node) bool {
		if result != nil {
			return false
		}
		if substitution, ok := current.(*syntax.CmdSubst); ok {
			result = substitution
			return false
		}
		return true
	})
	return result
}

// runBash parses a Bash script and records every external command in source order.
func runBash(t *testing.T, script string, request *Request, configure func(*Config, *[]*dispatchedCommand)) ([]*pathResult, []*dispatchedCommand, error) {
	t.Helper()
	file := parseForTest(t, script, "script.sh")
	var calls []*dispatchedCommand
	config := Config{MaxExecutionSteps: 100, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, command *Invocation) (*CommandResult, error) {
		args := argumentStrings(t, command)
		calls = append(calls, &dispatchedCommand{
			name: command.Name, args: args, stdin: string(command.Stdin), dir: command.Dir, env: maps.Clone(command.Env),
		})
		return &CommandResult{Stdout: []byte(command.Name + ":" + strings.Join(args, ",") + "\n")}, nil
	})}
	if configure != nil {
		configure(&config, &calls)
	}
	paths, _, executeErr := evaluateForTest(context.Background(), file, request, &config)
	return paths, calls, executeErr
}

func mustPath(t *testing.T, paths []*pathResult) *pathResult {
	t.Helper()
	if len(paths) != 1 {
		t.Fatalf("paths = %#v", paths)
	}
	return paths[0]
}

func requirePathStatus(t *testing.T, paths []*pathResult, want Status) {
	t.Helper()
	if len(paths) == 0 {
		t.Fatal("execution returned no paths")
	}
	for index := range paths {
		if paths[index].status != want {
			t.Fatalf("path %d status = %v, want %v; paths = %#v", index, paths[index].status, want, paths)
		}
	}
}

func TestExecuteBashLanguageFeatures(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		request    Request
		wantStatus Status
		wantCalls  []string
		check      func(*testing.T, []*pathResult, []*dispatchedCommand)
	}{
		{
			name: "assignments arrays declarations and append", script: `x=one; x+=two; a=(zero [2]=two); declare -A m=([k]=v); m[k]+=x; cmd "$x" "${a[2]}" "${m[k]}"`,
			wantStatus: StatusCompleted, wantCalls: []string{"cmd"},
			check: func(t *testing.T, _ []*pathResult, calls []*dispatchedCommand) {
				wantArgs(t, calls, []string{"onetwo", "two", "vx"})
			},
		},
		{
			name: "function positional local shift and return", script: `f() { local x=local; shift; cmd "$#" "$1" "$x"; return 7; }; f first second`,
			wantStatus: StatusCompleted, wantCalls: []string{"cmd"},
			check: func(t *testing.T, paths []*pathResult, calls []*dispatchedCommand) {
				wantArgs(t, calls, []string{"1", "second", "local"})
				got, _ := mustPath(t, paths).state.exitStatus.Data()
				if got != 7 {
					t.Fatalf("exit code = %d", got)
				}
			},
		},
		{
			name: "if case loops arithmetic and controls", script: `if true; then cmd if; fi; case abc in a*) cmd case;; esac; for x in one two; do cmd "$x"; done; n=0; while ((n < 2)); do ((n++)); cmd while; done; until true; do cmd never; done`,
			wantStatus: StatusCompleted, wantCalls: []string{"cmd", "cmd", "cmd", "cmd", "cmd", "cmd"},
			check: func(t *testing.T, _ []*pathResult, calls []*dispatchedCommand) {
				if got := []string{calls[0].args[0], calls[1].args[0], calls[2].args[0], calls[3].args[0], calls[4].args[0]}; !reflect.DeepEqual(got, []string{"if", "case", "one", "two", "while"}) {
					t.Fatalf("args = %#v", got)
				}
			},
		},
		{
			name: "logical negation and pipelines", script: `false || cmd or; true && cmd and; ! false && cmd negated; cmd left | cmd right; cmd pipeerr |& cmd sink`,
			wantStatus: StatusCompleted, wantCalls: []string{"cmd", "cmd", "cmd", "cmd", "cmd", "cmd", "cmd"},
			check: func(t *testing.T, _ []*pathResult, calls []*dispatchedCommand) {
				if calls[4].stdin != "cmd:left\n" || calls[6].stdin != "cmd:pipeerr\n" {
					t.Fatalf("pipeline stdin = %#v", calls)
				}
			},
		},
		{
			name: "subshell background wait", script: `(cmd child); cmd bg & wait; pwd`,
			wantStatus: StatusCompleted, wantCalls: []string{"cmd", "cmd"},
			check: func(t *testing.T, _ []*pathResult, calls []*dispatchedCommand) {
				if calls[0].dir != "/" || calls[1].dir != "/" {
					t.Fatalf("dirs = %#v", calls)
				}
			},
		},
		{
			name: "substitutions virtual input and redirections", script: `echo data > in; cmd "$(echo value)" < <(echo ignored); cmd "$(<in)" <<< hello > out; cmd again >> out; cmd err 2> errs; cmd both > combined 2>&1; read first second < out; cmd "$first" "$second"`,
			wantStatus: StatusCompleted, wantCalls: []string{"cmd", "cmd", "cmd", "cmd", "cmd", "cmd"},
			check: func(t *testing.T, paths []*pathResult, calls []*dispatchedCommand) {
				if mustPath(t, paths).state.issue != nil || calls[0].stdin != "ignored\n" || !reflect.DeepEqual(calls[5].args, []string{"cmd:data", ""}) {
					t.Fatalf("paths=%#v calls=%#v", paths, calls)
				}
			},
		},
	}
	for index := range tests {
		test := &tests[index]
		t.Run(test.name, func(t *testing.T) {
			paths, calls, err := runBash(t, test.script, &test.request, nil)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			requirePathStatus(t, paths, test.wantStatus)
			gotNames := make([]string, len(calls))
			for i := range calls {
				gotNames[i] = calls[i].name
			}
			if !reflect.DeepEqual(gotNames, test.wantCalls) {
				t.Fatalf("calls = %#v, want %v", gotNames, test.wantCalls)
			}
			if test.check != nil {
				test.check(t, paths, calls)
			}
		})
	}
}

func TestExecuteDispatchAndTerminalErrors(t *testing.T) {
	tests := []struct {
		name      string
		script    string
		configure func(*Config, *[]*dispatchedCommand)
		status    Status
		wantErr   bool
	}{
		{"handled dispatch completes", "unknown arg", nil, StatusCompleted, false},
		{"stop dispatch", "one; two", func(c *Config, _ *[]*dispatchedCommand) {
			c.LookupCommand = lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
				return &CommandResult{Action: CommandStop}, nil
			})
		}, StatusTerminated, false},
		{"handler error", "broken", func(c *Config, _ *[]*dispatchedCommand) {
			c.LookupCommand = lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
				return nil, errors.New("dispatch failed")
			})
		}, StatusIncomplete, true},
		{"budget", "one; two", func(c *Config, _ *[]*dispatchedCommand) { c.MaxExecutionSteps = 1 }, StatusIncomplete, false},
		{"unsupported declaration", "declare -z x=1", nil, StatusUnresolved, false},
		{"arbitrary output descriptor", "echo x 3> file", nil, StatusCompleted, false},
		{"host expansion branches", "if [[ $RANDOM -gt 1 ]]; then one; else two; fi", nil, StatusCompleted, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths, _, err := runBash(t, test.script, &Request{}, test.configure)
			if (err != nil) != test.wantErr {
				t.Fatalf("err = %v", err)
			}
			requirePathStatus(t, paths, test.status)
		})
	}

}

func TestRuntimePureHelpersAndState(t *testing.T) {
	if invertTruth(truthUnknown) != truthUnknown || andTruth(truthTrue, truthUnknown) != truthUnknown || andTruth(truthFalse, truthUnknown) != truthFalse || orTruth(truthFalse, truthUnknown) != truthUnknown || orTruth(truthTrue, truthUnknown) != truthTrue {
		t.Fatal("truth combinators")
	}
	if numericTruth(syntax.TsLss, 1, 2) != truthTrue || numericTruth(syntax.TsGtr, 1, 2) != truthFalse || boolExitCode(true) != 0 || boolExitCode(false) != 1 {
		t.Fatal("numeric truth")
	}

	s := newState(&Request{Env: map[string]string{"X": "old"}}, defaultMaxMemoryBytes)
	s.pushLocalScope()
	s.saveLocal("X")
	s.vars.put("X", expand.Variable{Set: true, Kind: expand.String, Str: "new"})
	s.saveLocal("Y")
	s.vars.put("Y", expand.Variable{Set: true, Kind: expand.String, Str: "temporary"})
	s.popLocalScope()
	if s.vars.Get("X").String() != "old" || s.vars.Get("Y").IsSet() {
		t.Fatalf("local restore = %#v", s.vars.data)
	}
	copy := s.vars.clone()
	copy.put("X", expand.Variable{Set: true, Kind: expand.String, Str: "copy"})
	if s.vars.Get("X").String() != "old" {
		t.Fatal("variable copy-on-write")
	}
	if err := s.vars.Set("X", expand.Variable{}); err != nil || s.vars.Get("X").IsSet() {
		t.Fatalf("variable unset = %v %#v", err, s.vars.Get("X"))
	}

	fs := newMemoryFS(defaultMaxMemoryBytes)
	if err := fs.ensureDir("/a/b"); err != nil {
		t.Fatal(err)
	}
	if err := fs.write("/a/b/file", []byte("one"), false); err != nil {
		t.Fatal(err)
	}
	clone := fs.clone()
	clone.write("/a/b/file", []byte("two"), true)
	resolved := fs.resolve("/a", "../a/b/file")
	contents, _ := fs.readValue("/a/b/file")
	clonedContents, _ := clone.readValue("/a/b/file")
	nullContents, _ := fs.readValue("/dev/null")
	if resolved != "/a/b/file" || string(contents) != "one" || string(clonedContents) != "onetwo" || len(nullContents) != 0 {
		t.Fatalf("memory fs = %#v %#v", fs, clone)
	}

	file := parseForTest(t, "echo value\n", "position.sh")
	gotLocation := sourceLocation(file.Stmts[0])
	if gotLocation.line != 1 || gotLocation.column != 1 || sourceLocation(nil) != unknownLocation {
		t.Fatalf("source location = %#v", gotLocation)
	}
	if !hostVariableUnknown(s, "RANDOM") || !hostVariableUnknown(s, "UID") {
		t.Fatal("host variable recognition")
	}
	s.vars.put("UID", expand.Variable{Set: true, Kind: expand.String, Str: "1000"})
	if hostVariableUnknown(s, "UID") {
		t.Fatal("set host identity should be known")
	}
	unknownFile := parseForTest(t, "echo $RANDOM; ((x=RANDOM))", "unknown.sh")
	if !wordHasHostUnknown(s, unknownFile.Stmts[0].Cmd.(*syntax.CallExpr).Args[1]) || !arithmHasHostUnknown(s, unknownFile.Stmts[1].Cmd.(*syntax.ArithmCmd).X) || !arithmMayMutate(unknownFile.Stmts[1].Cmd.(*syntax.ArithmCmd).X) {
		t.Fatal("host detection")
	}
}

func TestRuntimeExpansionAndRedirectionHelpers(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 10, &Request{})
	if got := appendVariable(expand.Variable{Set: true, Kind: expand.String, Str: "a"}, expand.Variable{Set: true, Kind: expand.String, Str: "b"}); got.String() != "ab" {
		t.Fatalf("string append = %#v", got)
	}
	if got := appendVariable(expand.Variable{Set: true, Kind: expand.Indexed, List: []string{"a"}}, expand.Variable{Set: true, Kind: expand.Indexed, List: []string{"b"}}); !reflect.DeepEqual(got.List, []string{"a", "b"}) {
		t.Fatalf("array append = %#v", got)
	}
	if got := appendVariable(expand.Variable{Set: true, Kind: expand.Associative, Map: map[string]string{"a": "1"}}, expand.Variable{Set: true, Kind: expand.Associative, Map: map[string]string{"b": "2"}}); got.Map["b"] != "2" {
		t.Fatalf("map append = %#v", got)
	}

	redirs := parseForTest(t, "echo x 2>&1 >out", "redirect.sh")
	for _, redir := range redirs.Stmts[0].Redirs {
		if _, err := redirectFD(redir); err != nil {
			t.Fatalf("redirect fd: %v", err)
		}
	}
	s.vars.put("one", expand.Variable{Set: true, Kind: expand.String, Str: "a b"})
	wordFile := parseForTest(t, "echo >$one", "word.sh")
	if _, _, err := e.redirectWord(s, wordFile.Stmts[0].Redirs[0].Word); err == nil {
		t.Fatal("ambiguous redirect accepted")
	}
}

func TestExecuteAdditionalBranches(t *testing.T) {
	tests := []struct {
		name   string
		script string
		status Status
		check  func(*testing.T, []*pathResult, []*dispatchedCommand)
	}{
		{
			name: "unknown case forks through catch all", script: `case "$RANDOM" in x*) one;; *) two;; esac`, status: StatusCompleted,
			check: func(t *testing.T, _ []*pathResult, calls []*dispatchedCommand) {
				if got := []string{calls[0].name, calls[1].name}; !reflect.DeepEqual(got, []string{"one", "two"}) {
					t.Fatalf("case calls = %#v", calls)
				}
			},
		},
		{
			name: "c style for break and continue", script: `for ((i=0; i<4; i++)); do if ((i == 1)); then continue; fi; cmd "$i"; if ((i == 2)); then break; fi; done`, status: StatusCompleted,
			check: func(t *testing.T, _ []*pathResult, calls []*dispatchedCommand) {
				if got := []string{calls[0].args[0], calls[1].args[0]}; !reflect.DeepEqual(got, []string{"0", "2"}) {
					t.Fatalf("loop calls = %#v", calls)
				}
			},
		},
		{
			name: "redirections including input append stderr and here document", script: `echo first > out; echo second >> out; read a b < out; cmd "$a" "$b" 2> err; cmd merged > both 2>&1; read only <<EOF
heredoc
EOF
cmd "$only" <<< trailing`, status: StatusCompleted,
			check: func(t *testing.T, _ []*pathResult, calls []*dispatchedCommand) {
				if !reflect.DeepEqual(calls[0].args, []string{"first", ""}) || calls[2].stdin != "trailing\n" {
					t.Fatalf("redirection calls = %#v", calls)
				}
			},
		},
		{
			name: "builtin malformed arguments", script: `printf; pwd x; cd a b; read -r x; unset -q x; shift x; wait 1; break 2; continue 999; return`, status: StatusCompleted,
		},
		{
			name: "host arithmetic mutation unresolved", script: `((x=RANDOM))`, status: StatusUnresolved,
		},
		{
			name: "command substitution and conditional branches", script: `cmd "$(echo substituted)"; if false; then missed; else cmd else; fi; false && missed; true || missed; [ missing`, status: StatusCompleted,
			check: func(t *testing.T, _ []*pathResult, calls []*dispatchedCommand) {
				if len(calls) != 2 || calls[0].args[0] != "substituted" || calls[1].args[0] != "else" {
					t.Fatalf("calls = %#v", calls)
				}
			},
		},
		{
			name: "unknown case no match and loop failures", script: `case "$RANDOM" in x*) one;; esac; for ((i=RANDOM; i<1; i++)); do missed; done`, status: StatusUnresolved,
			check: func(t *testing.T, paths []*pathResult, calls []*dispatchedCommand) {
				if len(calls) != 1 || calls[0].name != "one" || len(paths) != 2 {
					t.Fatalf("paths=%#v calls=%#v", paths, calls)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths, calls, err := runBash(t, test.script, &Request{}, nil)
			if err != nil {
				t.Fatalf("paths=%#v err=%v", paths, err)
			}
			requirePathStatus(t, paths, test.status)
			if test.check != nil {
				test.check(t, paths, calls)
			}
		})
	}
}

func TestCommandSubstitutionAndInternalErrors(t *testing.T) {
	file := parseForTest(t, `echo "$(emit value)"`, "substitution.sh")
	substitution := firstCommandSubstitution(file.Stmts[0].Cmd)
	if substitution == nil {
		t.Fatal("substitution not found")
	}
	s := newState(&Request{}, defaultMaxMemoryBytes)
	dispatches := 0
	e := &ExecutionContext{ctx: context.Background(), config: Config{MaxExecutionSteps: 10, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, command *Invocation) (*CommandResult, error) {
		dispatches++
		return &CommandResult{Stdout: []byte(strings.Join(argumentStrings(t, command), " "))}, nil
	})}}
	paths, err := e.evaluateStatement(s, file.Stmts[0])
	if err != nil || len(paths) != 1 || string(paths[0].state.stdout.data) != "value\n" || dispatches != 1 {
		t.Fatalf("paths=%#v err=%v dispatches=%d", paths, err, dispatches)
	}

	multipath := parseForTest(t, `echo "$(if [[ $RANDOM ]]; then one; else two; fi)"`, "multi.sh")
	multiPaths, err := e.evaluateStatement(newState(&Request{}, defaultMaxMemoryBytes), multipath.Stmts[0])
	if err != nil || len(multiPaths) != 2 {
		t.Fatalf("multi-path substitution paths=%#v err=%v", multiPaths, err)
	}

	inputOnly := parseForTest(t, `echo "$(<input)"`, "input.sh")
	s.fs.write("/input", []byte("contents\n"), false)
	result, failures, ok, err := e.inputOnlySubstitution(s, firstCommandSubstitution(inputOnly.Stmts[0].Cmd))
	if err != nil || result.stdout.unresolved || result.exitStatus.unresolved || failures != nil || !ok || string(result.stdout.data) != "contents\n" {
		t.Fatalf("input substitution = %#v err=%v ok=%t", result, err, ok)
	}

	badFD := &syntax.Redirect{N: &syntax.Lit{Value: "name"}}
	if _, err := redirectFD(badFD); err == nil {
		t.Fatal("named descriptor accepted")
	}
}

func TestTruthAndExpansionBranches(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 100, &Request{Env: map[string]string{"SET": "yes"}})
	s.fs.write("/file", []byte("x"), false)
	s.fs.ensureDir("/dir")
	for _, test := range []struct {
		script string
		want   truthValue
	}{
		{`[[ -z "" ]]`, truthTrue}, {`[[ -n value ]]`, truthTrue}, {`[[ -v SET ]]`, truthTrue},
		{`[[ ! -v MISSING ]]`, truthTrue}, {`[[ abc == a* ]]`, truthTrue}, {`[[ abc != z* ]]`, truthTrue},
		{`[[ a < b ]]`, truthTrue}, {`[[ b > a ]]`, truthTrue}, {`[[ 3 -eq 3 ]]`, truthTrue},
		{`[[ 3 -ne 4 ]]`, truthTrue}, {`[[ 3 -le 3 ]]`, truthTrue}, {`[[ 4 -ge 3 ]]`, truthTrue},
		{`[[ 2 -lt 3 && 4 -gt 3 ]]`, truthTrue}, {`[[ 0 -eq 1 || 4 -gt 3 ]]`, truthTrue},
	} {
		file := parseForTest(t, test.script, "truth.sh")
		got, err := e.testTruth(s, file.Stmts[0].Cmd.(*syntax.TestClause).X)
		if err != nil || got != test.want {
			t.Fatalf("%s = %v, %v", test.script, got, err)
		}
	}
	file := parseForTest(t, `a[1]=one; declare -A m; m[key]=value; a[-1]=bad`, "assign.sh")
	for _, statement := range file.Stmts[:3] {
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if ok {
			if err := e.applyAssignments(s, call.Assigns, expand.Unknown, false); err != nil {
				t.Fatal(err)
			}
			continue
		}
		declaration := statement.Cmd.(*syntax.DeclClause)
		var assignments []*syntax.Assign
		for _, assignment := range declaration.Args {
			if assignment.Name != nil {
				assignments = append(assignments, assignment)
			}
		}
		if err := e.applyAssignments(s, assignments, expand.Associative, false); err != nil {
			t.Fatal(err)
		}
	}
	if s.vars.Get("a").List[1] != "one" || s.vars.Get("m").Map["key"] != "value" {
		t.Fatalf("assignments = %#v", s.vars.data)
	}
	if err := e.applyAssignments(s, file.Stmts[3].Cmd.(*syntax.CallExpr).Assigns, expand.Unknown, false); err == nil {
		t.Fatal("negative index accepted")
	}

	v := newVariables(nil)
	v.put("metadata", expand.Variable{Set: true, Exported: false, Local: false, ReadOnly: false, Kind: expand.String, Str: "value"})
	if err := v.Set("metadata", expand.Variable{Set: true, Kind: expand.KeepValue, Exported: true, Local: true, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if got := v.Get("metadata"); got.String() != "value" || !got.Exported || !got.Local || !got.ReadOnly {
		t.Fatalf("keep-value metadata = %#v", got)
	}
}

func TestParameterExpansionOperandDemand(t *testing.T) {
	tests := []struct {
		name     string
		operator syntax.ParExpOperator
		value    expand.Variable
		want     bool
	}{
		{"alternate unset with value", syntax.AlternateUnset, expand.Variable{Set: true, Kind: expand.String, Str: "value"}, true},
		{"alternate null with empty", syntax.AlternateUnsetOrNull, expand.Variable{Set: true, Kind: expand.String}, false},
		{"default unset without value", syntax.DefaultUnset, expand.Variable{}, true},
		{"default null with empty", syntax.DefaultUnsetOrNull, expand.Variable{Set: true, Kind: expand.String}, true},
		{"error unset with value", syntax.ErrorUnset, expand.Variable{Set: true, Kind: expand.String, Str: "value"}, false},
		{"assign null with value", syntax.AssignUnsetOrNull, expand.Variable{Set: true, Kind: expand.String, Str: "value"}, false},
		{"other operator", syntax.RemSmallPrefix, expand.Variable{}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := newState(&Request{}, defaultMaxMemoryBytes)
			if test.value.Declared() {
				s.vars.put("x", test.value)
			}
			parameter := &syntax.ParamExp{
				Param: &syntax.Lit{Value: "x"},
				Exp:   &syntax.Expansion{Op: test.operator, Word: coverageWord("operand")},
			}
			if got := parameterExpansionWordNeeded(s, parameter); got != test.want {
				t.Fatalf("parameterExpansionWordNeeded()=%t, want %t", got, test.want)
			}
		})
	}

	t.Run("nameref resolves to unset target", func(t *testing.T) {
		s := newState(&Request{}, defaultMaxMemoryBytes)
		s.vars.put("ref", expand.Variable{Set: true, Kind: expand.NameRef, Str: "missing"})
		parameter := &syntax.ParamExp{
			Param: &syntax.Lit{Value: "ref"},
			Exp:   &syntax.Expansion{Op: syntax.DefaultUnset, Word: coverageWord("operand")},
		}
		if !parameterExpansionWordNeeded(s, parameter) {
			t.Fatal("unset nameref target treated as set")
		}
	})

	t.Run("indexed parameters are evaluated conservatively", func(t *testing.T) {
		parameter := &syntax.ParamExp{Param: &syntax.Lit{Value: "x"}, Index: coverageWord("0")}
		if !parameterExpansionWordNeeded(newState(&Request{}, defaultMaxMemoryBytes), parameter) {
			t.Fatal("indexed operand skipped")
		}
	})
}

func TestElementAppendPreservesContainerState(t *testing.T) {
	file := parseForTest(t, `a[0]+=bar; m[k]+=bar`, "element-append.sh")
	s := newState(&Request{}, defaultMaxMemoryBytes)
	s.vars.put("a", expand.Variable{
		Set:   true,
		Local: true,
		Kind:  expand.Indexed,
		List:  []string{"foo", "preserved"},
	})
	s.vars.put("m", expand.Variable{
		Set:      true,
		Exported: true,
		Local:    true,
		Kind:     expand.Associative,
		Map:      map[string]string{"k": "foo", "other": "preserved"},
	})
	e := &ExecutionContext{ctx: context.Background(), config: Config{MaxExecutionSteps: 10}}

	indexed := file.Stmts[0].Cmd.(*syntax.CallExpr).Assigns
	if err := e.applyAssignments(s, indexed, expand.Unknown, true); err != nil {
		t.Fatal(err)
	}
	indexedValue := s.vars.Get("a")
	if !reflect.DeepEqual(indexedValue.List, []string{"foobar", "preserved"}) {
		t.Fatalf("indexed append list = %#v", indexedValue.List)
	}
	if !indexedValue.Exported || !indexedValue.Local || indexedValue.ReadOnly || indexedValue.Kind != expand.Indexed {
		t.Fatalf("indexed append metadata = %#v", indexedValue)
	}

	associative := file.Stmts[1].Cmd.(*syntax.CallExpr).Assigns
	if err := e.applyAssignments(s, associative, expand.Unknown, false); err != nil {
		t.Fatal(err)
	}
	associativeValue := s.vars.Get("m")
	if !reflect.DeepEqual(associativeValue.Map, map[string]string{"k": "foobar", "other": "preserved"}) {
		t.Fatalf("associative append map = %#v", associativeValue.Map)
	}
	if !associativeValue.Exported || !associativeValue.Local || associativeValue.ReadOnly || associativeValue.Kind != expand.Associative {
		t.Fatalf("associative append metadata = %#v", associativeValue)
	}
}

func TestElementAppendInitializesContainersWithoutLosingState(t *testing.T) {
	file := parseForTest(t, `a[1]+=value; m[key]+=value; x[0]+=bar`, "element-initialize.sh")
	e := &ExecutionContext{ctx: context.Background(), config: Config{MaxExecutionSteps: 10}}

	t.Run("declared indexed metadata", func(t *testing.T) {
		s := newState(&Request{}, defaultMaxMemoryBytes)
		s.vars.put("a", expand.Variable{
			Exported: true,
			Local:    true,
			Kind:     expand.Indexed,
		})
		if err := e.applyAssignments(s, file.Stmts[0].Cmd.(*syntax.CallExpr).Assigns, expand.Unknown, false); err != nil {
			t.Fatal(err)
		}
		got := s.vars.Get("a")
		if !reflect.DeepEqual(got.List, []string{"", "value"}) || !got.Set || got.Kind != expand.Indexed {
			t.Fatalf("indexed value = %#v", got)
		}
		if !got.Exported || !got.Local || got.ReadOnly {
			t.Fatalf("indexed metadata = %#v", got)
		}
	})

	t.Run("declared associative metadata", func(t *testing.T) {
		s := newState(&Request{}, defaultMaxMemoryBytes)
		s.vars.put("m", expand.Variable{
			Exported: true,
			Local:    true,
			Kind:     expand.Associative,
		})
		if err := e.applyAssignments(s, file.Stmts[1].Cmd.(*syntax.CallExpr).Assigns, expand.Unknown, false); err != nil {
			t.Fatal(err)
		}
		got := s.vars.Get("m")
		if !reflect.DeepEqual(got.Map, map[string]string{"key": "value"}) || !got.Set || got.Kind != expand.Associative {
			t.Fatalf("associative value = %#v", got)
		}
		if !got.Exported || !got.Local || got.ReadOnly {
			t.Fatalf("associative metadata = %#v", got)
		}
	})

	t.Run("scalar index zero value and metadata", func(t *testing.T) {
		s := newState(&Request{}, defaultMaxMemoryBytes)
		s.vars.put("x", expand.Variable{
			Set:      true,
			Exported: true,
			Local:    true,
			Kind:     expand.String,
			Str:      "foo",
		})
		if err := e.applyAssignments(s, file.Stmts[2].Cmd.(*syntax.CallExpr).Assigns, expand.Unknown, false); err != nil {
			t.Fatal(err)
		}
		got := s.vars.Get("x")
		if !reflect.DeepEqual(got.List, []string{"foobar"}) || got.String() != "foobar" || got.Kind != expand.Indexed {
			t.Fatalf("scalar append value = %#v", got)
		}
		if !got.Exported || !got.Local || got.ReadOnly {
			t.Fatalf("scalar append metadata = %#v", got)
		}
	})
}
