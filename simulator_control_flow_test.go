package libcommand

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"
)

func TestSimulatorExecutesShellCommandWrappers(t *testing.T) {
	source := `command lark-cli direct
eval 'lark-cli evaluated'
echo 'lark-cli sourced' > virtual.sh
source virtual.sh
builtin lark-cli hidden || lark-cli builtin-failed`
	requireFirstArguments(t, source, []string{"direct", "evaluated", "sourced", "builtin-failed"})
}

func TestSimulatorStopsParsingCommandOptionsAtDoubleDash(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "-p", recordFirstArgument(t, &calls))
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `command -- -p target`}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"target"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSimulatorComposesEnvCommandExecAndShellWrappers(t *testing.T) {
	source := `env TOKEN=env bash -c 'lark-cli "env:$TOKEN"'
command bash -c 'lark-cli command'
command env TOKEN=command-env bash -c 'lark-cli "command-env:$TOKEN"'
exec env TOKEN=exec-env bash -c 'lark-cli "exec-env:$TOKEN"'
lark-cli unreachable`
	requireFirstArguments(t, source, []string{"env:env", "command", "command-env:command-env", "exec-env:exec-env"})
}

func TestSimulatorDoesNotExpandRegularExecArgumentsTwice(t *testing.T) {
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentStrings(t, invocation)
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `n=0; exec lark-cli "$((n++))" "$n"`,
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"0", "1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorDoesNotExpandDynamicCommandArgumentsTwice(t *testing.T) {
	var got [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = append(got, argumentStrings(t, invocation))
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `name=lark-cli; n=0; "$name" "$((n++))"; lark-cli "$n"`,
	}); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"0"}, {"1"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorDoesNotReexpandDynamicCommandWithUnresolvedArguments(t *testing.T) {
	var got [][]*Argument
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = append(got, append([]*Argument(nil), invocation.Args...))
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `name=lark-cli; value=$(unknown-command); n=0; "$name" "$value" "$((n++))"; lark-cli "$n"`,
	}); err != nil {
		t.Fatal(err)
	}
	want := [][]*Argument{
		{{Kind: ArgumentUnresolved}, {Kind: ArgumentString, Value: "0"}},
		{{Kind: ArgumentString, Value: "1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorAppliesEvalPrefixAssignmentsTemporarily(t *testing.T) {
	source := `x=outer; x=inner eval 'y=created; lark-cli "$x"'; lark-cli "$x"; lark-cli "$y"`
	requireFirstArguments(t, source, []string{"inner", "outer", "created"})
}

func TestSimulatorRestoresEvalPrefixAssignmentAfterInnerMutation(t *testing.T) {
	source := `x=outer; x=temp eval 'x=inner; y=created'; lark-cli "$x:$y"`
	requireFirstArguments(t, source, []string{"outer:created"})
}

func TestSimulatorRestoresSourcePrefixAssignmentAfterInnerMutation(t *testing.T) {
	source := `printf '%s\n' 'x=inner; y=created' > sourced.sh
x=outer
x=temp source sourced.sh
lark-cli "$x:$y"`
	requireFirstArguments(t, source, []string{"outer:created"})
}

func TestSimulatorPipelineUsesParentPipefailOption(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{
			name: "disabled in parent",
			source: `set +o pipefail
false | { set -o pipefail; true; }
if [[ $? -eq 0 ]]; then lark-cli correct; else lark-cli wrong; fi`,
		},
		{
			name: "enabled in parent",
			source: `set -o pipefail
false | { set +o pipefail; true; }
if [[ $? -eq 1 ]]; then lark-cli correct; else lark-cli wrong; fi`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireFirstArguments(t, test.source, []string{"correct"})
		})
	}
}

func TestSimulatorPipelineDoesNotLeakUnknownVariables(t *testing.T) {
	source := `x=known
unknown-command | { x=$(unknown-command); true; }
lark-cli "$x"`
	requireFirstArguments(t, source, []string{"known"})
}

func TestSimulatorEnvWithoutCommandPrintsSortedEnvironment(t *testing.T) {
	source := `value=$(env -i B=two A=one); lark-cli "$value"`
	requireFirstArguments(t, source, []string{"A=one\nB=two"})
}

func TestSimulatorEnvWithoutCommandPropagatesUnknownEnvironment(t *testing.T) {
	var got ArgumentKind
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		if len(invocation.Args) != 1 {
			t.Fatalf("arguments = %#v, want one argument", invocation.Args)
		}
		got = invocation.Args[0].Kind
		return &CommandResult{}, nil
	})
	source := `set -a
value=$(unknown-command)
set +a
output=$(env)
lark-cli "$output"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if got != ArgumentUnresolved {
		t.Fatalf("argument kind = %d, want ArgumentUnresolved", got)
	}
}

func TestSimulatorEvaluatesOnlyDemandedCommandSubstitutions(t *testing.T) {
	source := `x=present
lark-cli "${x:-$(lark-cli should-not-run-default)}"
unset x
lark-cli "${x:+$(lark-cli should-not-run-alternate)}"`
	requireJoinedArguments(t, source, []string{"present", ""})
}

func TestSimulatorReevaluatesCommandSubstitutionsPerDynamicStatement(t *testing.T) {
	source := `for i in one two; do value=$(lark-cli "loop-$i"); done
f() { value=$(lark-cli "function-$1"); }
f one
f two`
	requireJoinedArguments(t, source, []string{"loop-one", "loop-two", "function-one", "function-two"})
}

func TestSimulatorPreservesAssignmentCommandSubstitutionStatus(t *testing.T) {
	source := `value=$(false)
if [[ $? -eq 0 ]]; then lark-cli zero; else lark-cli nonzero; fi`
	requireFirstArguments(t, source, []string{"nonzero"})
}

func TestSimulatorUsesBashArithmeticInNumericTests(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `unset n
if [[ $n -eq 0 ]]; then lark-cli unset; else lark-cli wrong; fi
n=2
if [[ n -eq 2 ]]; then lark-cli variable; else lark-cli wrong; fi
n=010
if [[ $n -eq 8 ]]; then lark-cli octal; else lark-cli wrong; fi
if [[ n -eq 8 ]]; then lark-cli variable-octal; else lark-cli wrong; fi
if [[ 1+1 -eq 2 ]]; then lark-cli expression; else lark-cli wrong; fi
if [[ 010+1 -eq 9 ]]; then lark-cli expression-octal; else lark-cli wrong; fi
if [[ 16#ff -eq 255 ]]; then lark-cli explicit-base; else lark-cli wrong; fi
n=08
if [[ $n -eq 8 ]]; then lark-cli wrong; else lark-cli invalid; fi
if [[ n -eq 8 ]]; then lark-cli wrong; else lark-cli variable-invalid; fi
if [[ 08+0 -eq 8 ]]; then lark-cli wrong; else lark-cli expression-invalid; fi
lark-cli after`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"unset", "variable", "octal", "variable-octal", "expression", "expression-octal", "explicit-base", "invalid", "variable-invalid", "expression-invalid", "after"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorScalarAssignmentPreservesIndexedArrayTail(t *testing.T) {
	var calls [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, argumentStrings(t, invocation))
		return &CommandResult{}, nil
	})
	source := `values=(one two)
values=zero
lark-cli assign "${values[@]}"
values+=tail
lark-cli append "${values[@]}"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"assign", "zero", "two"}, {"append", "zerotail", "two"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorClearsErrexitInCommandSubstitutionByDefault(t *testing.T) {
	source := `set -e
value=$(false; echo survived)
lark-cli "$value"`
	requireFirstArguments(t, source, []string{"survived"})
}

func TestSimulatorHonorsCommandSubstitutionInheritanceOptions(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `shopt -s inherit_errexit
if shopt -q inherit_errexit; then lark-cli enabled; fi
set -e
value=$(false; echo missed)
lark-cli unreachable`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"enabled"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorInheritsErrTrapOnlyWithErrtrace(t *testing.T) {
	tests := []struct {
		name   string
		option string
		want   []string
	}{
		{name: "default", want: []string{"err", "after"}},
		{name: "errtrace", option: "set -E", want: []string{"err", "err", "after"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
			source := test.option + `
trap 'lark-cli err' ERR
value=$(false)
trap - ERR
lark-cli after`
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("calls = %#v, want %#v", calls, test.want)
			}
		})
	}
}

func TestSimulatorScopesErrTrapForFunctionsAndSubshells(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{name: "function default", source: `f() { false; true; }; f`, want: nil},
		{name: "function errtrace", source: `set -E; f() { false; true; }; f`, want: []string{"err"}},
		{name: "function final failure default", source: `f() { false; }; f`, want: []string{"err"}},
		{name: "function final failure errtrace", source: `set -E; f() { false; }; f`, want: []string{"err", "err"}},
		{name: "subshell default", source: `(false; true)`, want: nil},
		{name: "subshell errtrace", source: `set -E; (false; true)`, want: []string{"err"}},
		{name: "subshell final failure default", source: `(false)`, want: nil},
		{name: "subshell final failure errtrace", source: `set -E; (false)`, want: []string{"err"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
			source := "trap 'lark-cli err' ERR\n" + test.source + "\ntrap - ERR\nlark-cli after"
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
				t.Fatal(err)
			}
			want := append(append([]string(nil), test.want...), "after")
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %#v, want %#v", calls, want)
			}
		})
	}
}

func TestSimulatorExploresCommandSubstitutionOutputsInForItems(t *testing.T) {
	source := `for value in $(if [[ $RANDOM ]]; then echo first; else echo second; fi); do lark-cli "$value"; done`
	requireFirstArguments(t, source, []string{"first", "second"})
}

func TestSimulatorRestoresOnlyTemporaryAssignmentNames(t *testing.T) {
	var invocations []*Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		invocations = append(invocations, &Invocation{
			Args: append([]*Argument(nil), invocation.Args...),
			Env:  maps.Clone(invocation.Env),
			Dir:  invocation.Dir,
		})
		return &CommandResult{}, nil
	})

	source := `x=old
TMP=temporary read x <<< new
lark-cli "$x" "$TMP"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(invocations) != 1 {
		t.Fatalf("invocations = %#v", invocations)
	}
	got := invocations[0]
	if !reflect.DeepEqual(argumentStrings(t, got), []string{"new", ""}) || got.Dir != "/" || got.Env["PWD"] != "/" {
		t.Fatalf("invocation = %#v", got)
	}
}

func TestSimulatorHonorsFunctionDeclarationScope(t *testing.T) {
	source := `x=outer
f() { local x; lark-cli "local:$x"; declare x=inner; }
f
lark-cli "global:$x"`
	requireFirstArguments(t, source, []string{"local:", "global:outer"})
}

func TestSimulatorRestoresAbsentPositionalArgumentsAfterFunction(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "record", recordFirstArgument(t, &calls))
	source := `f() { :; }; f argument; record "${1-unset}"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"unset"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorRejectsReadonlyReassignment(t *testing.T) {
	callbacks := 0
	simulator := mustBuildSimulator(t, "lark-cli", countInvocations(&callbacks))

	err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `readonly x=safe; x=evil; lark-cli "$x"`})
	if err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Fatalf("Simulate() error = %v, want readonly error", err)
	}
	if callbacks != 0 {
		t.Fatalf("callbacks = %d, want zero", callbacks)
	}
}

func TestSimulatorRejectsReadonlyReadAndUnset(t *testing.T) {
	source := `readonly x=safe y=stable
unset x
lark-cli unset "$?" "$x"
read y <<< changed
lark-cli read "$?" "$y"`
	requireJoinedArguments(t, source, []string{"unset 1 safe", "read 0 stable"})
}

func TestSimulatorRollsBackExpansionBeforeSubstitutionRetry(t *testing.T) {
	var args []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		if len(invocation.Args) == 1 && argumentString(t, invocation.Args[0]) == "produce" {
			return &CommandResult{Stdout: []byte("generated\n")}, nil
		}
		args = argumentStrings(t, invocation)
		return &CommandResult{}, nil
	})

	source := `n=0; lark-cli "$((n++))" "$(lark-cli produce)" "$n"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"0", "generated", "1"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args=%#v, want=%#v", args, want)
	}
}

func TestSimulatorExploresSubstitutionOutputsAcrossExpansionContexts(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordJoinedArguments(t, &calls))

	source := `if [[ "$(if [[ $RANDOM ]]; then echo first; else echo second; fi)" == first ]]; then lark-cli test-true; else lark-cli test-false; fi
case "$(if [[ $RANDOM ]]; then echo first; else echo second; fi)" in first) lark-cli case-first;; second) lark-cli case-second;; esac`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"test-true", "test-false", "case-first", "case-second", "case-first", "case-second"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%#v, want=%#v", calls, want)
	}
}

func TestSimulatorEvaluatesSubstitutionBeforeReportingHostUnknownWord(t *testing.T) {
	var calls [][]*Argument
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, append([]*Argument(nil), invocation.Args...))
		if len(invocation.Args) == 1 && *invocation.Args[0] == (Argument{Kind: ArgumentString, Value: "nested"}) {
			return &CommandResult{Stdout: []byte("generated\n")}, nil
		}
		return &CommandResult{}, nil
	})

	err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `lark-cli outer "$RANDOM$(lark-cli nested)"`})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]*Argument{
		{{Kind: ArgumentString, Value: "nested"}},
		{{Kind: ArgumentString, Value: "outer"}, {Kind: ArgumentUnresolved}},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%#v, want=%#v", calls, want)
	}
}

func TestSimulatorPreservesLazyDoubleBracketLogicalEvaluation(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))

	source := `if [[ -n "" && -n "$(lark-cli should-not-run)" ]]; then :; fi
if [[ -n value || -n "$(lark-cli should-not-run-either)" ]]; then :; fi`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("calls=%#v, want none", calls)
	}
}

func TestSimulatorEvaluatesSubstitutionInUnknownExpressions(t *testing.T) {
	tests := []struct {
		name   string
		source string
		stdout string
	}{
		{name: "double bracket operand", source: `[[ "$RANDOM" == "$(probe)" ]]`, stdout: "candidate\n"},
		{name: "arithmetic command", source: `(( RANDOM + $(probe) ))`, stdout: "1\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			callbacks := 0
			simulator := mustBuildSimulator(t, "probe", func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
				callbacks++
				return &CommandResult{Stdout: []byte(test.stdout)}, nil
			})
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if callbacks != 1 {
				t.Fatalf("callbacks=%d, want 1", callbacks)
			}
		})
	}
}

func TestSimulatorReturnsFailureWithoutShadowingReadonlyLocal(t *testing.T) {
	requireFirstArguments(t, `readonly x=global; f() { local x; }; f; lark-cli "$x:$?"`, []string{"global:1"})
}

func TestSimulatorDoesNotReplayEarlierCaseBodyForLaterPatternSubstitution(t *testing.T) {
	source := `case value in
value) lark-cli first ;;&
"$(if [[ $RANDOM ]]; then echo value; else echo other; fi)") lark-cli second ;;
esac`
	requireFirstArguments(t, source, []string{"first", "second"})
}

func TestSimulatorReevaluatesCStyleLoopSubstitutions(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   int
	}{
		{"condition", `for ((i=0; i<2 && $(probe); i++)); do :; done`, 2},
		{"post expression", `for ((i=0; i<2; i+=$(probe))); do :; done`, 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			callbacks := 0
			simulator := mustBuildSimulator(t, "probe", func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
				callbacks++
				return &CommandResult{Stdout: []byte("1\n")}, nil
			})
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if callbacks != test.want {
				t.Fatalf("callbacks=%d, want %d", callbacks, test.want)
			}
		})
	}
}

func TestSimulatorPropagatesCStyleLoopSubstitutionHandlerError(t *testing.T) {
	sentinel := errors.New("condition handler failed")
	simulator := mustBuildSimulator(t, "probe", func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
		return nil, sentinel
	})
	err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `for ((i=0; $(probe); i++)); do :; done`})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Simulate() error=%v, want sentinel", err)
	}
}

func TestSimulatorPreservesDoubleBracketTestSemantics(t *testing.T) {
	source := `if [[ foo == "f*" ]]; then lark-cli quoted-match; else lark-cli quoted-literal; fi
echo data > virtual
if [[ -f virtual ]]; then lark-cli virtual-file; else lark-cli missing-file; fi
if [[ -e absent ]]; then lark-cli phantom-double-bracket; else lark-cli absent-double-bracket; fi
if test -e absent; then lark-cli phantom-test; else lark-cli absent-test; fi`
	requireFirstArguments(t, source, []string{"quoted-literal", "virtual-file", "absent-double-bracket", "absent-test"})
}

func TestSimulatorDeterminesSupportedTestPredicates(t *testing.T) {
	source := `if [ ! a = b ]; then lark-cli negated-true; else lark-cli negated-false; fi
printf x > regular
if [ -x regular ]; then lark-cli regular-executable; else lark-cli regular-not-executable; fi
if [ -w / ]; then lark-cli directory-writable; else lark-cli directory-not-writable; fi`
	requireFirstArguments(t, source, []string{"negated-true", "regular-not-executable", "directory-writable"})
}

func TestSimulatorTestsIndividualArrayElementsWithDashV(t *testing.T) {
	source := `a=([2]=two)
if [[ -v 'a[2]' ]]; then lark-cli indexed-present; else lark-cli wrong; fi
if [[ -v 'a[1]' ]]; then lark-cli wrong; else lark-cli indexed-missing; fi
declare -A m=([key]=value)
if [[ -v 'm[key]' ]]; then lark-cli associative-present; else lark-cli wrong; fi`
	requireFirstArguments(t, source, []string{"indexed-present", "indexed-missing", "associative-present"})
}

func TestSimulatorHandlesDeterministicTestErrorsAndRegex(t *testing.T) {
	source := `if [ invalid -eq 1 ]; then lark-cli wrong-number; else lark-cli invalid-number; fi
if [[ abc =~ ^a ]]; then lark-cli regex-match; else lark-cli wrong-match; fi
if [[ abc =~ z$ ]]; then lark-cli wrong-miss; else lark-cli regex-miss; fi
if [[ abc123 =~ ^([a-z]+)([0-9]+)$ ]]; then
  lark-cli "captures:${BASH_REMATCH[0]}:${BASH_REMATCH[1]}:${BASH_REMATCH[2]}"
fi
if [[ abc =~ [ ]]; then lark-cli wrong-regex; else lark-cli invalid-regex; fi`
	requireFirstArguments(t, source, []string{
		"invalid-number",
		"regex-match",
		"regex-miss",
		"captures:abc123:abc:123",
		"invalid-regex",
	})
}

func TestSimulatorUsesPOSIXRegularExpressionSemantics(t *testing.T) {
	source := `if [[ 1 =~ \d ]]; then lark-cli perl-digit; else lark-cli ordinary-escape-false; fi
if [[ aa =~ (a|aa) ]]; then lark-cli "${BASH_REMATCH[0]}:${BASH_REMATCH[1]}"; fi`
	requireFirstArguments(t, source, []string{"ordinary-escape-false", "aa:aa"})
}

func TestSimulatorCorrelatesUnknownCDStateWithStatus(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		value := invocation.Args[1].Value
		if invocation.Args[1].Kind == ArgumentUnresolved {
			value = "<unresolved>"
		}
		calls = append(calls, invocation.Args[0].Value+":"+value)
		return &CommandResult{}, nil
	})
	source := `if cd /candidate; then
  lark-cli success "$PWD"
else
  lark-cli failure "$PWD"
fi`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"success:<unresolved>", "failure:/"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorCorrelatesUnknownRedirectionStateWithStatus(t *testing.T) {
	source := `if : > /candidate/file; then
  lark-cli redirection-success
else
  if [ -e /candidate/file ]; then lark-cli redirection-impossible; else lark-cli redirection-failure; fi
fi`
	requireFirstArguments(t, source, []string{"redirection-success", "redirection-failure"})
}

func TestSimulatorSupportsEchoNoNewlineAndEmptySourceStatus(t *testing.T) {
	source := `value=$(echo -n chat-id)
lark-cli "$value"
false
eval ''
if [[ $? -eq 0 ]]; then lark-cli eval-zero; fi
: > empty.sh
false
source empty.sh
if [[ $? -eq 0 ]]; then lark-cli source-zero; fi`
	requireFirstArguments(t, source, []string{"chat-id", "eval-zero", "source-zero"})
}

func TestSimulatorTreatsConcreteNestedSyntaxErrorsAsCommandFailures(t *testing.T) {
	source := `eval 'if' || lark-cli eval-failed
bash -c 'if' || lark-cli bash-failed
sh -c 'if' || lark-cli sh-failed
lark-cli after`
	requireFirstArguments(t, source, []string{"eval-failed", "bash-failed", "sh-failed", "after"})
}

func TestSimulatorFailsOpenUnsupportedShellBuiltins(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{"history", `history -c`},
		{"builtin history", `builtin history -c`},
		{"alias", `alias ll='lark-cli aliased'`},
		{"ulimit", `ulimit -n`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := make(map[string]int)
			simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
				calls[argumentString(t, invocation.Args[0])]++
				return &CommandResult{}, nil
			})
			source := "if " + test.source + "; then lark-cli success; else lark-cli failure; fi"
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
				t.Fatal(err)
			}
			if calls["success"] != 1 || calls["failure"] != 1 {
				t.Fatalf("calls = %#v, want one success and one failure", calls)
			}
		})
	}
}

func TestSimulatorExecutesFiniteLoops(t *testing.T) {
	callbacks := 0
	simulator := mustBuildSimulator(t, "lark-cli", countInvocations(&callbacks))
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `for i in {1..30}; do lark-cli "$i"; done`}); err != nil {
		t.Fatal(err)
	}
	if callbacks != 30 {
		t.Fatalf("callbacks = %d", callbacks)
	}
}

func TestSimulatorTreatsMissingSelectInputAsEOF(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `select choice in one two; do lark-cli "$choice"; done || lark-cli eof`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"eof"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSimulatorConsumesConcreteSelectInput(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `select choice in one two; do
  lark-cli "$choice:$REPLY"
  if [[ $REPLY == x ]]; then break; fi
done`
	request := &SimulationRequest{Source: source, Stdin: []byte("\n2\nx\n")}
	if err := simulator.Simulate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"two:2", ":x"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSimulatorSelectReturnsFailureAtEndOfInput(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `select choice in one two; do lark-cli "$choice"; done || lark-cli eof`
	request := &SimulationRequest{Source: source, Stdin: []byte("1\n2\n")}
	if err := simulator.Simulate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"one", "two", "eof"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSimulatorTreatsExplicitEmptySelectInputAsEOF(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `select choice in one two; do lark-cli "$choice"; done || lark-cli eof`
	request := &SimulationRequest{Source: source, Stdin: []byte{}}
	if err := simulator.Simulate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"eof"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSimulatorHonorsNestedLoopControlDepth(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name: "break two loops",
			source: `for a in 1 2; do
  for b in 1 2; do lark-cli before "$a$b"; break 2; done
  lark-cli outer "$a"
done
lark-cli end`,
			want: []string{"before", "end"},
		},
		{
			name: "continue outer loop",
			source: `for a in 1 2; do
  for b in 1 2; do lark-cli before "$a$b"; continue 2; done
  lark-cli outer "$a"
done
lark-cli end`,
			want: []string{"before", "before", "end"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
				calls = append(calls, argumentString(t, invocation.Args[0]))
				return &CommandResult{}, nil
			})
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("calls = %#v, want %#v", calls, test.want)
			}
		})
	}
}

func TestSimulatorRejectsInvalidLoopControlWithoutStopping(t *testing.T) {
	source := `break || lark-cli outer-break
continue || lark-cli outer-continue
for value in one; do
  break 0 || lark-cli invalid-break
  lark-cli after-break
  break
done
for value in one; do
  continue 0 || lark-cli invalid-continue
  lark-cli after-continue
  break
done`
	requireFirstArguments(t, source, []string{"outer-break", "outer-continue", "invalid-break", "after-break", "invalid-continue", "after-continue"})
}

func TestSimulatorStopsScopesOnInvalidNumericExitAndReturn(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{name: "exit", source: `exit bad; lark-cli unreachable`},
		{
			name:   "return",
			source: `f() { return bad; lark-cli unreachable; }; f; lark-cli after`,
			want:   []string{"after"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireFirstArguments(t, test.source, test.want)
		})
	}
}

func TestSimulatorDoesNotContinueAfterFatalControlBuiltinErrors(t *testing.T) {
	for _, source := range []string{
		`f() { return 1 2; lark-cli unreachable; }; f; lark-cli after`,
		`for value in one; do break bad; lark-cli unreachable; done; lark-cli after`,
		`for value in one; do continue 1 2; lark-cli unreachable; done; lark-cli after`,
	} {
		requireFirstArguments(t, source, nil)
	}
}

func TestSimulatorHonorsCaseFallthroughOperators(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{"unconditional fallthrough", `case x in x) lark-cli first ;& y) lark-cli second ;; esac`},
		{"resume matching", `case x in x) lark-cli first ;;& x) lark-cli second ;; esac`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
				calls = append(calls, argumentString(t, invocation.Args[0]))
				return &CommandResult{}, nil
			})
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, []string{"first", "second"}) {
				t.Fatalf("calls = %#v", calls)
			}
		})
	}
}

func TestSimulatorSharesVirtualFilesAcrossProcessBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{"subshell", `(echo data > file); lark-cli "$(<file)"`},
		{"background", `echo data > file & wait; lark-cli "$(<file)"`},
		{"command substitution", `value=$(echo data > file); lark-cli "$(<file)"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var argument string
			simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
				argument = argumentString(t, invocation.Args[0])
				return &CommandResult{}, nil
			})
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if argument != "data" {
				t.Fatalf("argument = %q, want data", argument)
			}
		})
	}
}

func TestSimulatorWaitWithoutOperandsReturnsSuccess(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `false & wait && lark-cli yes || lark-cli no`}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"yes"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSimulatorCommandStopIsNormalAndGlobal(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, argumentString(t, invocation.Args[0]))
		return &CommandResult{Action: CommandStop}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `if [[ $RANDOM ]]; then lark-cli first; else lark-cli second; fi; lark-cli after`}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"first"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSimulatorCommandStopDoesNotMaskEarlierPathError(t *testing.T) {
	simulator := mustBuildSimulator(t, "stop", func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
		return &CommandResult{Action: CommandStop}, nil
	})
	tests := []struct {
		name   string
		source string
	}{
		{
			name:   "nested if condition",
			source: `if if [[ $RANDOM ]]; then echo x 3>bad; else stop; fi; then :; fi`,
		},
		{
			name:   "logical left operand",
			source: `if [[ $RANDOM ]]; then echo x 3>bad; else stop; fi && :`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source})
			if err == nil || !strings.Contains(err.Error(), "unsupported output file descriptor 3") {
				t.Fatalf("Simulate() error = %v", err)
			}
		})
	}
}
