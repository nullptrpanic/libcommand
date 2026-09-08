package libcommand

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
)

func TestSimulatorInputTransformsAdvanceConsumedStdin(t *testing.T) {
	for _, test := range []*struct {
		name, source, stdin, want string
	}{
		{name: "base64 encode", source: `base64 >/dev/null`, stdin: "abc\n", want: "1:"},
		{name: "base64 decode", source: `base64 -d >/dev/null`, stdin: "YWJjCg==", want: "1:"},
		{name: "base64 invalid data", source: `base64 -d >/dev/null`, stdin: "%%%\n", want: "1:"},
		{name: "base64 invalid option", source: `base64 --unsupported`, stdin: "abc\n", want: "0:abc"},
		{name: "base64 unsupported file", source: `base64 file`, stdin: "abc\n", want: "0:abc"},
		{name: "rev", source: `rev >/dev/null`, stdin: "abc\n", want: "1:"},
		{name: "rev unsupported file", source: `rev file`, stdin: "abc\n", want: "0:abc"},
		{name: "base64 in subshell", source: `(base64 >/dev/null)`, stdin: "abc\n", want: "1:"},
		{name: "rev in substitution", source: `value=$(rev)`, stdin: "abc\n", want: "1:"},
		{name: "redirected input does not consume parent", source: `base64 <<< temporary >/dev/null`, stdin: "abc\n", want: "0:abc"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
			err := simulator.Simulate(context.Background(), &SimulationRequest{
				Source: test.source + `; read value; lark-cli "$?:$value"`,
				Stdin:  []byte(test.stdin),
			})
			if err != nil || !reflect.DeepEqual(calls, []string{test.want}) {
				t.Fatalf("calls=%q, error=%v; want %q", calls, err, test.want)
			}
		})
	}
}

func TestSimulatorUnsetArraySubscripts(t *testing.T) {
	for _, test := range []*struct{ source, want string }{
		{`a=(zero one two); i=1; unset 'a[i]'; lark-cli "${!a[*]}:${a[*]}:$?"`, "0 2:zero two:0"},
		{`a=(zero one two); i=0; unset 'a[i+1]'; lark-cli "${!a[*]}"`, "0 2"},
		{`a=(zero one two); i=1; unset 'a[i++]'; lark-cli "$i:${!a[*]}"`, "2:0 2"},
		{`a=([2]=two [5]=five); unset 'a[2]'; lark-cli "${#a[@]}:${!a[*]}:${a[*]}"`, "1:5:five"},
		{`a=(zero one); unset 'a[@]'; lark-cli "${#a[@]}:${a[*]}"`, "0:"},
		{`a=(zero one); unset 'a[*]'; a[3]=three; lark-cli "${#a[@]}:${!a[*]}"`, "1:3"},
		{`a=(zero one); readonly a; unset 'a[@]'; lark-cli "$?:${a[*]}"`, "1:zero one"},
		{`a=(zero one); unset 'a[1/0]'; lark-cli "$?:${a[*]}"`, "1:zero one"},
	} {
		t.Run(test.source, func(t *testing.T) { requireFirstArguments(t, test.source, []string{test.want}) })
	}
}

func TestSimulatorExecPreservesArgvZero(t *testing.T) {
	for _, test := range []*struct{ source, want string }{
		{`exec -a fake bash -c 'lark-cli "$0"'`, "fake"},
		{`exec -l bash -c 'lark-cli "$0"'`, "-bash"},
		{`exec -a fake -l bash -c 'lark-cli "$0"'`, "-fake"},
		{`exec -l -a fake bash -c 'lark-cli "$0"'`, "-fake"},
		{`exec -a '' bash -c 'lark-cli "<$0>"'`, "<>"},
		{`exec -a fake bash -c 'lark-cli "$0"' explicit`, "explicit"},
		{`exec -a fake bash -c 'bash -c '\''lark-cli "$0"'\'''`, "bash"},
	} {
		t.Run(test.source, func(t *testing.T) { requireFirstArguments(t, test.source, []string{test.want}) })
	}
}

func TestSimulatorUnsetUnknownIndexPreservesUncertainty(t *testing.T) {
	var calls []*Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, invocation)
		return &CommandResult{}, nil
	})
	err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `a=(zero one); i=$(missing); unset 'a[i]'; lark-cli "${a[0]}" "${a[1]}"; unset 'a[@]'; lark-cli "${#a[@]}"`})
	if err != nil || len(calls) != 2 {
		t.Fatalf("calls=%v, error=%v", calls, err)
	}
	for _, argument := range calls[0].Args {
		if argument.Kind != ArgumentUnresolved {
			t.Fatalf("unknown deletion exposed a concrete element: %+v", argument)
		}
	}
	if len(calls[1].Args) != 1 || calls[1].Args[0].Kind != ArgumentString || calls[1].Args[0].Value != "0" {
		t.Fatalf("clearing the entire array must restore certainty: %+v", calls[1].Args)
	}
}

func TestSimulatorCatContinuesAfterDirectoryOperands(t *testing.T) {
	for _, test := range []*struct{ name, source, want string }{
		{name: "directory before file", source: `printf ok > file; output=$(cat / file); lark-cli "$?:$output"`, want: "1:ok"},
		{name: "directory between files", source: `printf a > first; printf b > second; output=$(cat first / second / first); lark-cli "$?:$output"`, want: "1:aba"},
		{name: "directory before stdin", source: `output=$(cat / - -); code=$?; read remainder; lark-cli "$code:$output:$?:$remainder"`, want: "1:input:1:"},
		{name: "known failure dominates unknown operand", source: `output=$(cat missing /); lark-cli "$?"`, want: "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
			err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source, Stdin: []byte("input\n")})
			if err != nil || !reflect.DeepEqual(calls, []string{test.want}) {
				t.Fatalf("calls=%q, error=%v; want %q", calls, err, test.want)
			}
		})
	}
}

func TestSimulatorNegativeShiftPreservesArguments(t *testing.T) {
	requireFirstArguments(t, `set -- a b; shift -1; lark-cli "$?:$#:$1:$2"`, []string{"1:2:a:b"})
	requireFirstArguments(t, `set -- a b; shift 3; lark-cli "$?:$#:$1:$2"`, []string{"1:2:a:b"})
	requireFirstArguments(t, `set -- a b; shift 1; lark-cli "$?:$#:$1"`, []string{"0:1:b"})
}

func TestSimulatorMalformedTestHasConcreteFailure(t *testing.T) {
	for _, expression := range []string{`[ x -eq ]`, `test x -eq`, `test -n x y`, `test a unsupported b`, `test '!' x -eq`} {
		t.Run(expression, func(t *testing.T) {
			requireFirstArguments(t, `if `+expression+`; then lark-cli unexpected; else lark-cli "$?"; fi; lark-cli after`, []string{"2", "after"})
		})
	}
}

func TestSimulatorTestOperandArity(t *testing.T) {
	for _, test := range []*struct{ expression, want string }{
		{`test '!' = x`, "1"},
		{`test '!' = '!'`, "0"},
		{`test '(' x ')'`, "0"},
		{`test '(' '' ')'`, "1"},
		{`test '!' x = x`, "1"},
		{`test '!' -n ''`, "0"},
	} {
		t.Run(test.expression, func(t *testing.T) {
			requireFirstArguments(t, test.expression+`; lark-cli "$?"`, []string{test.want})
		})
	}
	// Valid but unmodelled host predicates remain unknown, not syntax errors.
	for _, expression := range []string{`test -t 0`, `test a -nt b`} {
		requireFirstArguments(t, `if `+expression+`; then lark-cli yes; else lark-cli no; fi`, []string{"yes", "no"})
	}
}

func TestSimulatorCharacterDevicePredicatesUseVirtualPathKind(t *testing.T) {
	for _, expression := range []string{`test -c`, `[[ -c`} {
		for _, test := range []*struct{ path, want string }{{"file", "no"}, {"/", "no"}, {"/dev/null", "yes"}} {
			condition := expression + " " + test.path
			if strings.HasPrefix(expression, "[[") {
				condition += " ]]"
			}
			t.Run(condition, func(t *testing.T) {
				requireFirstArguments(t, `printf data > file; if `+condition+`; then lark-cli yes; else lark-cli no; fi`, []string{test.want})
			})
		}
	}
}

func TestSimulatorEmptyCommandExpansion(t *testing.T) {
	for _, test := range []*struct{ name, source, want string }{
		{"unquoted empty variable", `empty=; false; $empty; lark-cli "$?"`, "0"},
		{"empty substitution", `$(true); lark-cli "$?"`, "0"},
		{"failed empty substitution", `$(false); lark-cli "$?"`, "1"},
		{"assignment without executable", `empty=; value=kept $empty; lark-cli "$?:$value"`, "0:kept"},
		{"substitution assignment status", `value=$(false) $(true); lark-cli "$?:$value"`, "1:"},
		{"redirection without executable", `empty=; $empty > file; lark-cli "$?:$(cat file)"`, "0:"},
		{"quoted empty executable", `empty=; "$empty"; lark-cli "$?"`, "127"},
		{"temporary empty executable assignment", `empty=; value=outer; value=inner "$empty"; lark-cli "$?:$value"`, "127:outer"},
		{"later field supplies executable", `empty=; $empty lark-cli reached`, "reached"},
	} {
		t.Run(test.name, func(t *testing.T) { requireFirstArguments(t, test.source, []string{test.want}) })
	}
}

func TestSimulatorUnknownIFSDoesNotTaintLiteralExpansion(t *testing.T) {
	for _, test := range []*struct{ name, source, want string }{
		{"scalar copy", `copy=$value; lark-cli "$copy"`, "a:b"},
		{"substitution assignment", `copy=$(printf '%s' a:b); lark-cli "$copy"`, "a:b"},
		{"positional count assignment", `set -- one two; copy=${#*}; lark-cli "$copy"`, "2"},
		{"quoted positional count", `set -- one two; lark-cli "${#*}"`, "2"},
		{"quoted array count", `values=(a b); lark-cli "${#values[*]}"`, "2"},
		{"test operands", `if [[ $value == a:b ]]; then lark-cli yes; else lark-cli no; fi`, "yes"},
		{"case subject", `case $value in a:b) lark-cli yes;; *) lark-cli no;; esac`, "yes"},
		{"case pattern", `pattern='a:*'; case a:b in $pattern) lark-cli yes;; *) lark-cli no;; esac`, "yes"},
		{"inactive star join", `set -- one two; lark-cli "${value:-$*}"`, "a:b"},
		{"nested star join does not taint concrete output", `set -- one two; lark-cli "$(joined=$*; printf concrete)"`, "concrete"},
		{"unknown glob ignore in assignment", `GLOBIGNORE=$(unknown); value='*'; copy=$value; lark-cli "$copy"`, "*"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireFirstArguments(t, `value=a:b; IFS=$(unknown); `+test.source, []string{test.want})
		})
	}
}

func TestSimulatorLiteralStarJoinStillDependsOnUnknownIFS(t *testing.T) {
	for _, source := range []string{`joined=$*`, `joined="${values[*]}"`, `unset value; joined=${value:-$*}`} {
		t.Run(source, func(t *testing.T) {
			calls := 0
			simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
				calls++
				if len(invocation.Args) != 1 || invocation.Args[0].Kind != ArgumentUnresolved {
					t.Fatalf("arguments=%#v, want unknown joined value", invocation.Args)
				}
				return commandResultForTest(command, nil, nil, 0), nil
			})
			err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `set -- a b; values=(a b); IFS=$(unknown); ` + source + `; lark-cli "$joined"`})
			if err != nil || calls != 1 {
				t.Fatalf("calls=%d, error=%v", calls, err)
			}
		})
	}
}

func TestSimulatorUserHandlerOverridesShellBuiltin(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "echo", recordFirstArgument(t, &calls))
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `echo user`}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"user"}) {
		t.Fatalf("calls = %#v, want user handler invocation", calls)
	}
}

func TestSimulatorUserHandlerOverridesDeclarationAndLetInvocationForms(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		argument string
		sources  []string
	}{
		{
			name: "declare", command: "declare", argument: "value=1",
			sources: []string{
				`declare value=1`,
				`name=declare; "$name" value=1`,
				`command declare value=1`,
				`builtin declare value=1`,
			},
		},
		{
			name: "local", command: "local", argument: "value=1",
			sources: []string{
				`f() { local value=1; }; f`,
				`f() { name=local; "$name" value=1; }; f`,
				`f() { command local value=1; }; f`,
				`f() { builtin local value=1; }; f`,
			},
		},
		{
			name: "export", command: "export", argument: "value=1",
			sources: []string{
				`export value=1`,
				`name=export; "$name" value=1`,
				`command export value=1`,
				`builtin export value=1`,
			},
		},
		{
			name: "readonly", command: "readonly", argument: "value=1",
			sources: []string{
				`readonly value=1`,
				`name=readonly; "$name" value=1`,
				`command readonly value=1`,
				`builtin readonly value=1`,
			},
		},
		{
			name: "typeset", command: "typeset", argument: "value=1",
			sources: []string{
				`typeset value=1`,
				`name=typeset; "$name" value=1`,
				`command typeset value=1`,
				`builtin typeset value=1`,
			},
		},
		{
			name: "let", command: "let", argument: "value=1",
			sources: []string{
				`let 'value=1'`,
				`name=let; "$name" 'value=1'`,
				`command let 'value=1'`,
				`builtin let 'value=1'`,
			},
		},
	}

	for _, test := range tests {
		for index, source := range test.sources {
			t.Run(fmt.Sprintf("%s/form-%d", test.name, index), func(t *testing.T) {
				var invocations []*Invocation
				simulator := mustBuildSimulator(t, test.command, func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
					invocations = append(invocations, &Invocation{
						Name: invocation.Name,
						Args: append([]*Argument(nil), invocation.Args...),
					})
					return commandResultForTest(command, nil, nil, 0), nil
				})
				if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
					t.Fatal(err)
				}
				if len(invocations) != 1 {
					t.Fatalf("invocations = %#v, want one", invocations)
				}
				if invocations[0].Name != test.command {
					t.Fatalf("invocation name = %q, want %q", invocations[0].Name, test.command)
				}
				if arguments := argumentStrings(t, invocations[0]); !reflect.DeepEqual(arguments, []string{test.argument}) {
					t.Fatalf("arguments = %#v, want %#v", arguments, []string{test.argument})
				}
			})
		}
	}
}

func TestSimulatorUserSyntaxCommandOverridesDoNotRunDefaultSemantics(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{name: "declare", source: `declare value=default; observe "${value-unset}"`, want: "unset"},
		{name: "let", source: `value=0; let 'value=1'; observe "$value"`, want: "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			var observed []string
			simulator := NewBuilder().
				Command(test.name, countInvocations(&calls)).
				Command("observe", recordFirstArgument(t, &observed)).
				Build()
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !reflect.DeepEqual(observed, []string{test.want}) {
				t.Fatalf("calls = %d, observed = %#v, want one override and %q", calls, observed, test.want)
			}
		})
	}
}

func TestSimulatorExpandsOverriddenSyntaxCommandArguments(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   []string
	}{
		{name: "export", source: `source=resolved; export -x value="$source"`, want: []string{"-x", "value=resolved"}},
		{name: "let", source: `expression='value=2'; let "$expression"`, want: []string{"value=2"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			simulator := mustBuildSimulator(t, test.name, func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
				got = argumentStrings(t, invocation)
				return commandResultForTest(command, nil, nil, 0), nil
			})
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("arguments = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSimulatorExpandsStructuredDeclarationArgumentForOverride(t *testing.T) {
	var invocation *Invocation
	simulator := mustBuildSimulator(t, "declare", func(_ context.Context, command *CommandContext, current *Invocation) (*CommandResult, error) {
		invocation = current
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `declare -a values=(one two)`}); err != nil {
		t.Fatal(err)
	}
	if invocation == nil || !reflect.DeepEqual(invocation.Args, []*Argument{{Kind: ArgumentString, Value: "-a"}, {Kind: ArgumentString, Value: "values"}}) {
		t.Fatalf("invocation = %#v", invocation)
	}
}

func TestSimulatorCallsFunctionOverridingStructuredDeclaration(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{name: "array", source: `function declare { lark-cli "$@"; }; declare -a values=(one two)`, want: "-a values"},
		{name: "array append", source: `function declare { lark-cli "$@"; }; declare -a values+=(one two)`, want: "-a values+"},
		{name: "indexed", source: `function declare { lark-cli "$@"; }; index=2; value=two; declare values[$index]="$value"`, want: "values[2]=two"},
		{name: "indexed append", source: `function declare { lark-cli "$@"; }; declare values[2]+=two`, want: "values[2]+=two"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireJoinedArguments(t, test.source, []string{test.want})
		})
	}
}

func TestSimulatorExpandsStructuredDeclarationBeforeOverride(t *testing.T) {
	var calls []string
	simulator := NewBuilder().
		Command("produce", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			calls = append(calls, "produce")
			return commandResultForTest(command, []byte("one\n"), nil, 0), nil
		}).
		Command("declare", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
			calls = append(calls, "declare:"+strings.Join(argumentStrings(t, invocation), " "))
			return commandResultForTest(command, nil, nil, 0), nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `declare -a values=($(produce))`}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"produce", "declare:-a values"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorDoesNotReplayStructuredDeclarationSideEffects(t *testing.T) {
	requireJoinedArguments(t, `i=0; function declare { lark-cli "$i" "$@"; }; declare -a values=([i++]=$(echo one))`, []string{"1 -a values"})
}

func TestSimulatorDoesNotReplayLetArgumentSideEffects(t *testing.T) {
	requireJoinedArguments(t, `i=0; function let { lark-cli "$i" "$@"; }; let "$((i++))" "$(echo one)"`, []string{"1 0 one"})
}

func TestSimulatorUserHandlerOverridesEnvWrapper(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "env", recordFirstArgument(t, &calls))
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `env user`}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"user"}) {
		t.Fatalf("calls = %#v, want user handler invocation", calls)
	}
}

func TestSimulatorDispatchesExpandedCommands(t *testing.T) {
	var invocations []*Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		invocations = append(invocations, &Invocation{
			Name:  invocation.Name,
			Args:  append([]*Argument(nil), invocation.Args...),
			Env:   maps.Clone(invocation.Env),
			Dir:   invocation.Dir,
			Stdin: append([]byte(nil), invocation.Stdin...),
		})
		if reflect.DeepEqual(argumentStrings(t, invocation), []string{"produce"}) {
			return commandResultForTest(command, []byte("generated\n"), nil, 0), nil
		}
		return commandResultForTest(command, nil, nil, 0), nil
	})

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `value=hello; items=(zero one); lark-cli send "$value ${items[1]}"; lark-cli produce | lark-cli consume; lark-cli nested "$(lark-cli produce)"`,
		Env:    map[string]string{"TOKEN": "secret"},
		Stdin:  []byte("request input"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(invocations) != 5 {
		t.Fatalf("invocations = %#v", invocations)
	}
	if got := invocations[0]; got.Name != "lark-cli" || !reflect.DeepEqual(argumentStrings(t, got), []string{"send", "hello one"}) || got.Env["TOKEN"] != "secret" || got.Dir != "/" || string(got.Stdin) != "request input" {
		t.Fatalf("first invocation = %#v", got)
	}
	if got := invocations[2]; !reflect.DeepEqual(argumentStrings(t, got), []string{"consume"}) || string(got.Stdin) != "generated\n" {
		t.Fatalf("pipeline invocation = %#v", got)
	}
	if got := argumentStrings(t, invocations[4]); !reflect.DeepEqual(got, []string{"nested", "generated"}) {
		t.Fatalf("nested args = %#v", got)
	}
}

func TestSimulatorProvidesTopLevelArguments(t *testing.T) {
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentStrings(t, invocation)
		return commandResultForTest(command, nil, nil, 0), nil
	})

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `set -- "$@" tail; shift; lark-cli "$0" "$#" "$1" "$2" "$@"`,
		Args:   []string{"first", "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"command.sh", "2", "second", "tail", "second", "tail"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorUsesScriptCreatedVirtualFilesForSourceAndGlobbing(t *testing.T) {
	var calls [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, argumentStrings(t, invocation))
		return commandResultForTest(command, nil, nil, 0), nil
	})

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `printf '%s\n' 'PREFIX=loaded; lark-cli "inside:$1"' > /config.sh
: > a.json
: > b.json
source /config.sh sourced
lark-cli "$PREFIX:$1" *.json
shopt -s nullglob
lark-cli missing-*.txt`,
		Args: []string{"outer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"inside:sourced"}, {"loaded:outer", "a.json", "b.json"}, nil}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorSupportsCommonSetOptionsAndExit(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `set -uo pipefail
false | true || lark-cli pipefail
set +u
lark-cli "${missing}"
set -e
false || lark-cli recovered
if false; then lark-cli condition; fi
(false; lark-cli subshell-after-failure)
lark-cli parent-after-failure`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"pipefail", "", "recovered"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}

	calls = nil
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `exit 7; lark-cli unreachable`}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("calls after exit = %#v", calls)
	}
}

func TestSimulatorStartsNestedShellWithFreshExitStatus(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `false; bash -c 'lark-cli "$?"'`,
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"0"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorReadsNestedShellSourceFromStdin(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `printf '%s' 'lark-cli "bash:$0"' | bash
printf '%s' 'lark-cli "bash-s:$0:$1"' | bash -s first
printf '%s' 'lark-cli "sh:$0"' | sh
printf '%s' 'lark-cli "sh-s:$0:$1"' | sh -s second`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"bash:bash", "bash-s:bash:first", "sh:sh", "sh-s:sh:second"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorKeepsUnknownNestedShellStdinOpen(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `unknown-command | bash; lark-cli after`,
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"after"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorSupportsCommonReadForms(t *testing.T) {
	source := `read -r first
read -r second
mapfile -t remaining
lark-cli "$first" "$second" "${#remaining[@]}" "${remaining[1]}"`
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentStrings(t, invocation)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: source,
		Stdin:  []byte("first\\value\nsecond value\nthird\nfourth\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"first\\value", "second value", "2", "fourth"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorPreservesReadIFSDelimitersAndEmptyFields(t *testing.T) {
	var calls [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, argumentStrings(t, invocation))
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `IFS=: read -r first rest <<<'one:two:three'
lark-cli "$first" "$rest"
IFS=, read -r a b c <<<'x,,z'
lark-cli "$a" "$b" "$c"
IFS=' ' read -r first rest <<<'  one  two   three  '
lark-cli "$first" "$rest"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"one", "two:three"}, {"x", "", "z"}, {"one", "two   three"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorSplitsReadFieldsUsingMultibyteIFS(t *testing.T) {
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentStrings(t, invocation)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	const source = `IFS=é read -r first second <<<'aéb'
lark-cli "$first" "$second"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorPrintfRetainsOutputAfterInvalidDynamicWidth(t *testing.T) {
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentStrings(t, invocation)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `printf -v value 'prefix:%*s:suffix' bad y
status=$?
lark-cli "$value" "$status"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"prefix:y:suffix", "1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorWrapsExitAndReturnStatuses(t *testing.T) {
	source := `f() { return 300; }
f
lark-cli "return:$?"
trap 'lark-cli "exit:$?"' EXIT
exit -1`
	requireFirstArguments(t, source, []string{"return:44", "exit:255"})
}

func TestSimulatorReadsMissingInputRedirectionAsEmptyFile(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `if lark-cli called < missing; then
  lark-cli wrong
else
  lark-cli fallback
fi`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"called", "wrong"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorReadsMissingInputOnlySubstitutionAsEmptyFile(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `if value=$(<missing); then lark-cli wrong; else lark-cli fallback; fi`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"wrong"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorRejectsOutputConflictsAndCreatesMissingParents(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{name: "directory target", source: `lark-cli side-effect >/ `, want: []string{"fallback"}},
		{name: "missing parent", source: `lark-cli side-effect >/missing/target`, want: []string{"side-effect", "wrong"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
			source := "if " + test.source + "; then lark-cli wrong; else lark-cli fallback; fi"
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("calls = %#v, want %#v", calls, test.want)
			}
		})
	}
}

func TestSimulatorAppliesOutputRedirectionsLeftToRight(t *testing.T) {
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentStrings(t, invocation)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `echo hi >/a >/b
read -r a </a
read -r b </b
lark-cli "a=$a" "b=$b"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"a=", "b=hi"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorExploresUnspecifiedDirectoriesAndRejectsCreatedFileConflicts(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `if cd /missing; then lark-cli possible; else lark-cli missing; fi`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"possible", "missing"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}

	calls = nil
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `: > /blocked; if cd /blocked; then lark-cli wrong; else lark-cli blocked; fi`}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"blocked"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("file conflict calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorPreservesBuiltinWritesThroughPrefixAssignments(t *testing.T) {
	var invocations []*Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		invocations = append(invocations, &Invocation{
			Args: append([]*Argument(nil), invocation.Args...),
			Env:  maps.Clone(invocation.Env),
			Dir:  invocation.Dir,
		})
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `export READ_VALUE=outer PRINT_VALUE=outer
READ_VALUE=prefix read READ_VALUE <<< prefix
PRINT_VALUE=prefix printf -v PRINT_VALUE body
lark-cli "$READ_VALUE" "$PRINT_VALUE"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(invocations) != 1 {
		t.Fatalf("invocations = %#v", invocations)
	}
	got := invocations[0]
	if want := []string{"prefix", "body"}; !reflect.DeepEqual(argumentStrings(t, got), want) {
		t.Fatalf("args = %#v, want %#v", got.Args, want)
	}
	if got.Env["READ_VALUE"] != "prefix" || got.Env["PRINT_VALUE"] != "body" || got.Env["PWD"] != "/" || got.Dir != "/" {
		t.Fatalf("invocation = %#v", got)
	}
}

func TestSimulatorPreservesVariableAttributesOnBuiltinAssignment(t *testing.T) {
	var environment map[string]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		environment = maps.Clone(invocation.Env)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `export READ_VALUE=old PRINT_VALUE=old
read READ_VALUE <<< new
printf -v PRINT_VALUE updated
lark-cli`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if environment["READ_VALUE"] != "new" || environment["PRINT_VALUE"] != "updated" {
		t.Fatalf("environment = %#v", environment)
	}
}

func TestSimulatorSupportsCommandLookupLetAndTime(t *testing.T) {
	source := `if command -v lark-cli >/dev/null; then lark-cli found; fi
if type missing >/dev/null 2>&1; then lark-cli wrong; else lark-cli missing; fi
let 'value = 1 + 2'
time lark-cli "$value"`
	requireFirstArguments(t, source, []string{"found", "missing", "3"})
}

func TestSimulatorUsesBashIntegerWrappingAndPreservesArithmeticPrefixMutations(t *testing.T) {
	source := `lark-cli "$((9223372036854775808))" "$((18446744073709551615))" "$((18446744073709551616))" "$((16#ffffffffffffffff))" "$((16#10000000000000000))"
x=0
((x=1,1/0))
lark-cli "arithmetic:$?:$x"
x=0
let 'x=1,1/0'
lark-cli "let:$?:$x"`
	requireJoinedArguments(t, source, []string{
		"-9223372036854775808 -1 0 -1 0",
		"arithmetic:1:1",
		"let:1:1",
	})
}

func TestSimulatorUsesBashArithmeticControlAndRecursiveVariables(t *testing.T) {
	source := `value='1+2'
x=0
lark-cli "$((2 ? 1 : 0))" "$((0 && (x=1)))" "$((value))" "$x"`
	requireJoinedArguments(t, source, []string{"1 0 3 0"})
}

func TestSimulatorUsesConsistentDevNullSemantics(t *testing.T) {
	source := `source /dev/../dev/null && lark-cli sourced
[[ -e /dev/null ]] && lark-cli exists
[[ -f /dev/null ]] || lark-cli not-regular
[[ -c /dev/null ]] && lark-cli character
test -e /dev/null && lark-cli builtin-exists
test -c /dev/null && lark-cli builtin-character
cd /dev/null || lark-cli not-directory`
	requireFirstArguments(t, source, []string{"sourced", "exists", "not-regular", "character", "builtin-exists", "builtin-character", "not-directory"})
}

func TestSimulatorAdvancesReadAndGetoptsBeforeReadonlyAssignmentFailure(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `readonly read_target option
read read_target
read_status=$?
read next
lark-cli "read:$read_status:$next"
set -- -a -b
getopts ab option
getopts_status=$?
getopts ab next_option
lark-cli "getopts:$getopts_status:$next_option:$OPTIND"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: source,
		Stdin:  []byte("first\nsecond\n"),
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"read:0:second", "getopts:1:b:3"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorKeepsGetoptsProgressWhenOPTINDIsReadonly(t *testing.T) {
	source := `readonly OPTIND=1
set -- -a -b
getopts ab first
getopts ab second
lark-cli "$first:$second:$OPTIND"`
	requireFirstArguments(t, source, []string{"a:b:1"})
}

func TestSimulatorAdvancesGetoptsBeforeInvalidTargetFailure(t *testing.T) {
	source := `set -- -a -b
getopts ab bad-name
first_status=$?
getopts ab option
lark-cli "$first_status:$option:$OPTIND"`
	requireFirstArguments(t, source, []string{"1:b:3"})
}

func TestSimulatorReadContinuesPastReadonlyTargets(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `readonly middle=old
read first middle last
lark-cli "$?:$first:$middle:$last"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: source,
		Stdin:  []byte("one two three\n"),
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"0:one:old:three"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorFindsSupportedShellBuiltins(t *testing.T) {
	source := `command -v getopts >/dev/null && lark-cli getopts
command -v declare >/dev/null && lark-cli declare
command -v export >/dev/null && lark-cli export`
	requireFirstArguments(t, source, []string{"getopts", "declare", "export"})
}

func TestSimulatorRunsAndClearsExitTraps(t *testing.T) {
	source := `cleanup() { lark-cli cleanup; }
trap cleanup EXIT
lark-cli body`
	requireFirstArguments(t, source, []string{"body", "cleanup"})

	source = `trap 'lark-cli skipped' EXIT; trap - EXIT; lark-cli body`
	requireFirstArguments(t, source, []string{"body"})

	source = `trap 'case $? in 0) lark-cli success;; *) lark-cli failure;; esac' EXIT
unknown-command
exit`
	requireFirstArguments(t, source, []string{"success", "failure"})
}

func TestSimulatorPreservesExitStatusAfterExitTrap(t *testing.T) {
	source := `bash -c 'trap "lark-cli child-trap" EXIT; exit 7' || lark-cli fallback
bash -c 'trap "exit 3" EXIT; exit 7'
lark-cli "override:$?"`
	requireFirstArguments(t, source, []string{"child-trap", "fallback", "override:3"})
}

func TestSimulatorRunsOnlyChildLocalExitTrapsAtProcessBoundaries(t *testing.T) {
	source := `trap 'lark-cli parent' EXIT
( trap 'lark-cli subshell' EXIT; : )
value=$(trap 'lark-cli substitution' EXIT; printf value)
lark-cli "body:$value"`
	requireFirstArguments(t, source, []string{"subshell", "substitution", "body:value", "parent"})
}

func TestSimulatorRunsExitTrapsForBackgroundAndProcessSubstitutions(t *testing.T) {
	source := `( trap 'lark-cli background' EXIT; : ) &
wait
lark-cli consume < <(trap 'lark-cli process-input' EXIT; printf data)
printf output > >(trap 'lark-cli process-output' EXIT; read value; lark-cli "process-output:$value")`
	requireFirstArguments(t, source, []string{"background", "process-input", "consume", "process-output:output", "process-output"})
}

func TestSimulatorRunsErrTrapsOutsideConditionalContexts(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `trap 'lark-cli "err:$?"' ERR
false
lark-cli after
false || true
if false; then :; fi
unknown-command
lark-cli unknown-after`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	want := []string{"err:1", "after", "err:1", "unknown-after", "unknown-after"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}

	calls = nil
	source = `set -e; trap 'lark-cli err' ERR; false; lark-cli unreachable`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"err"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorSupportsCommonOutputRedirections(t *testing.T) {
	source := `echo combined &>combined.log
echo stderr 2>stderr.log >&2
lark-cli "$(<combined.log):$(<stderr.log)"`
	requireFirstArguments(t, source, []string{"combined:stderr"})
}

func TestSimulatorSupportsProcessSubstitution(t *testing.T) {
	var calls []*Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, &Invocation{
			Args:  append([]*Argument(nil), invocation.Args...),
			Stdin: append([]byte(nil), invocation.Stdin...),
		})
		if len(invocation.Args) > 0 && argumentString(t, invocation.Args[0]) == "produce" {
			return commandResultForTest(command, []byte("generated\n"), nil, 0), nil
		}
		return commandResultForTest(command, nil, nil, 0), nil
	})

	source := `lark-cli consume < <(lark-cli produce)
printf sink-data > >(lark-cli sink)`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %#v", calls)
	}
	if !reflect.DeepEqual(argumentStrings(t, calls[0]), []string{"produce"}) || !reflect.DeepEqual(argumentStrings(t, calls[1]), []string{"consume"}) || string(calls[1].Stdin) != "generated\n" {
		t.Fatalf("input process substitution calls = %#v", calls[:2])
	}
	if !reflect.DeepEqual(argumentStrings(t, calls[2]), []string{"sink"}) || string(calls[2].Stdin) != "sink-data" {
		t.Fatalf("output process substitution call = %#v", calls[2])
	}
}

func TestProcessSubstitutionDoesNotOverwriteUserFiles(t *testing.T) {
	var calls [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, argumentStrings(t, invocation))
		return commandResultForTest(command, nil, nil, 0), nil
	})

	source := `printf user > /.libcommand-process-1
lark-cli consume <(printf generated)
lark-cli "$(< /.libcommand-process-1)"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %#v", calls)
	}
	if want := []string{"consume", "/.libcommand-process-2"}; !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("process substitution args = %#v, want %#v", calls[0], want)
	}
	if want := []string{"user"}; !reflect.DeepEqual(calls[1], want) {
		t.Fatalf("preserved file args = %#v, want %#v", calls[1], want)
	}
}

func TestSimulatorSupportsGetopts(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `while getopts ':ab:' option; do
  case $option in
    a) lark-cli a ;;
    b) lark-cli "b:$OPTARG" ;;
    :) lark-cli "missing:$OPTARG" ;;
    \?) lark-cli "unknown:$OPTARG" ;;
  esac
done
shift "$((OPTIND - 1))"
lark-cli "rest:$1"`
	request := &SimulationRequest{Source: source, Args: []string{"-a", "-bvalue", "tail"}}
	if err := simulator.Simulate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b:value", "rest:tail"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorRestartsGetoptsWhenOPTINDIsReassigned(t *testing.T) {
	source := `set -- -ab
getopts ab option
lark-cli "first:$option:$OPTIND"
OPTIND=1
getopts ab option
lark-cli "reset:$option:$OPTIND"
getopts ab option
lark-cli "next:$option:$OPTIND"`
	requireFirstArguments(t, source, []string{"first:a:1", "reset:a:1", "next:b:2"})
}

func TestSimulatorQueriesShellOptionsWithoutMutatingThem(t *testing.T) {
	source := `shopt -u extglob
shopt -s >/dev/null
shopt -q extglob
lark-cli "unchanged:$?"
shopt extglob >/dev/null
lark-cli "off:$?"
shopt -s extglob
shopt extglob >/dev/null
lark-cli "on:$?"
set -xv
snapshot=$(set +o)
lark-cli "$snapshot"`
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || calls[0] != "unchanged:1" || calls[1] != "off:1" || calls[2] != "on:0" {
		t.Fatalf("calls = %#v", calls)
	}
	if !strings.Contains(calls[3], "set +o errexit") || !strings.Contains(calls[3], "set +o pipefail") {
		t.Fatalf("set +o output = %q", calls[3])
	}
	if !strings.Contains(calls[3], "set -o verbose") || !strings.Contains(calls[3], "set -o xtrace") {
		t.Fatalf("set +o trace output = %q", calls[3])
	}
}

func TestSimulatorSupportsBuiltinQueriesAndCDOptions(t *testing.T) {
	source := `trap 'lark-cli cleanup' EXIT
lark-cli "$(trap -p EXIT)" "$(type -t lark-cli)" "$(type -t echo)"
cd -L /
cd -- /
unset HOME
cd || lark-cli "cd:$?:$PWD"`
	var calls [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, argumentStrings(t, invocation))
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"trap -- 'lark-cli cleanup' EXIT", "file", "builtin"},
		{"cd:1:/"},
		{"cleanup"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorStopsPrintfAtPercentBEscapeC(t *testing.T) {
	requireFirstArguments(t, `lark-cli "start$(printf '%bX' '\cignored')end"`, []string{"startend"})
}

func TestSimulatorPropagatesImplicitUnknownExpansionInputs(t *testing.T) {
	var got *Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = &Invocation{Args: append([]*Argument(nil), invocation.Args...)}
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `value='left:right'
IFS=$(unknown-command)
HOME=$(unknown-command)
lark-cli "$value" $value ~ ~root`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Args) != 4 {
		t.Fatalf("invocation = %#v, want four arguments", got)
	}
	if got.Args[0].Kind != ArgumentString || got.Args[0].Value != "left:right" {
		t.Fatalf("quoted argument = %#v", got.Args[0])
	}
	for index := 1; index < len(got.Args); index++ {
		if got.Args[index].Kind != ArgumentUnresolved {
			t.Fatalf("argument %d = %#v, want unresolved", index, got.Args[index])
		}
	}
}

func TestSimulatorExpandsPositionalStarUsingIFS(t *testing.T) {
	requireJoinedArguments(t, `set -- left right; IFS=:; lark-cli "$*" "${#*}"`, []string{"left:right 2"})
}

func TestSimulatorExpandsEmptyPositionalAtWithoutArgument(t *testing.T) {
	var got *Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = &Invocation{Args: append([]*Argument(nil), invocation.Args...)}
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `set --; lark-cli "$@"`}); err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Args) != 0 {
		t.Fatalf("invocation = %#v, want no arguments", got)
	}
}

func TestSimulatorPropagatesUnknownIFSForStarAndRead(t *testing.T) {
	var calls []*Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, &Invocation{Args: append([]*Argument(nil), invocation.Args...)})
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `IFS=$(unknown-command)
set -- left right
lark-cli "$*"
values=(left right)
lark-cli "${values[*]}"
read first second <<<'left:right'
lark-cli "$first" "$second"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %#v, want three invocations", calls)
	}
	for index, call := range calls[:2] {
		if len(call.Args) != 1 || call.Args[0].Kind != ArgumentUnresolved {
			t.Fatalf("call %d arguments = %#v, want one unresolved argument", index, call.Args)
		}
	}
	if len(calls[2].Args) != 2 {
		t.Fatalf("read arguments = %#v, want two arguments", calls[2].Args)
	}
	for index, argument := range calls[2].Args {
		if argument.Kind != ArgumentUnresolved {
			t.Fatalf("read argument %d = %#v, want unresolved", index, argument)
		}
	}
}

func TestSimulatorExecClearEnvironmentAndAcceptsIdentityOptions(t *testing.T) {
	var environments []map[string]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		environments = append(environments, maps.Clone(invocation.Env))
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `export TOKEN=secret; exec -c lark-cli`}); err != nil {
		t.Fatal(err)
	}
	if len(environments) != 1 || len(environments[0]) != 0 {
		t.Fatalf("environments = %#v, want one empty environment", environments)
	}
	for _, source := range []string{
		`lark-cli "$(exec -c env)"`,
		`lark-cli "$(command exec -c env)"`,
		`lark-cli "$(builtin exec -c env)"`,
	} {
		requireFirstArguments(t, source, []string{""})
	}
	for _, source := range []string{`exec -a custom lark-cli`, `exec -l lark-cli`} {
		environments = nil
		err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source})
		if err != nil {
			t.Fatalf("source %q error = %v", source, err)
		}
		if len(environments) != 1 {
			t.Fatalf("source %q environments = %#v", source, environments)
		}
	}
}

func TestSimulatorExploresFailureOfUnknownExecTarget(t *testing.T) {
	requireFirstArguments(t, `exec unknown-command || lark-cli fallback
lark-cli after`, []string{"fallback", "after"})
	var environment map[string]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		environment = maps.Clone(invocation.Env)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `export TOKEN=secret; exec -c unknown-command || lark-cli fallback`,
	}); err != nil {
		t.Fatal(err)
	}
	if environment["TOKEN"] != "secret" {
		t.Fatalf("fallback environment = %#v, want original exported TOKEN", environment)
	}
}

func TestSimulatorKeepsBuiltinSemanticsAcrossInvocationForms(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "literal let", source: `let 'x=1'`},
		{name: "dynamic let", source: `name=let; "$name" 'x=1'`},
		{name: "command let", source: `command let 'x=1'`},
		{name: "builtin let", source: `builtin let 'x=1'`},
		{name: "literal declare", source: `x=value; declare -a x`},
		{name: "dynamic declare", source: `x=value; name=declare; "$name" -a x`},
		{name: "command declare", source: `x=value; command declare -a x`},
		{name: "builtin declare", source: `x=value; builtin declare -a x`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			suffix := `; lark-cli "$x"`
			want := "1"
			if strings.Contains(test.name, "declare") {
				suffix = `; lark-cli "${x[0]-unset}"`
				want = "value"
			}
			requireFirstArguments(t, test.source+suffix, []string{want})
		})
	}
}

func TestSimulatorPreservesScalarWhenPromotingToIndexedArray(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{name: "array append", source: `x=value; x+=(tail); lark-cli "${x[@]}" "${#x[@]}"`, want: "value tail 2"},
		{name: "naked declaration", source: `x=value; declare -a x; x+=(tail); lark-cli "${x[@]}" "${#x[@]}"`, want: "value tail 2"},
		{name: "declaration append", source: `x=value; declare -a x+=tail; lark-cli "${x[@]}" "${#x[@]}"`, want: "valuetail 1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireJoinedArguments(t, test.source, []string{test.want})
		})
	}
}

func TestSimulatorExpandsDirectDeclarationOnlyOnce(t *testing.T) {
	requireJoinedArguments(t, `index=0; declare value=$((index++)); lark-cli "$value" "$index"`, []string{"0 1"})
}

func TestSimulatorDoesNotRetainDeferredDeclarationExpansionAsIssue(t *testing.T) {
	simulator := NewBuilder().Command("broken", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
		return nil, fmt.Errorf("later handler failure")
	}).Build()
	for _, source := range []string{
		`declare value="$(echo resolved)"; broken`,
		`let "value = $(echo 1)"; broken`,
	} {
		err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source})
		if err == nil || !strings.Contains(err.Error(), "later handler failure") {
			t.Fatalf("source %q: Simulate() error = %v, want the later handler error", source, err)
		}
	}
}

func TestSimulatorDoesNotChargeDeferredLetExpansionTwice(t *testing.T) {
	calls := 0
	simulator := NewBuilder().
		Limits(&Limits{MaxExecutionSteps: 3}).
		Command("lark-cli", countInvocations(&calls)).
		Build()
	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `let "value = $(echo 1)"; lark-cli done`,
	})
	if err != nil || calls != 1 {
		t.Fatalf("Simulate() error = %v, calls = %d, want one call within three steps", err, calls)
	}
}

func TestSimulatorTreatsDeclarationUsageErrorsAsKnownFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{name: "direct option", source: `declare -z value || lark-cli "$?"`, want: "2"},
		{name: "dynamic option", source: `name=declare; "$name" -z value || lark-cli "$?"`, want: "2"},
		{name: "dynamic identifier", source: `name=export; "$name" bad-name=value || lark-cli "$?"`, want: "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireFirstArguments(t, test.source, []string{test.want})
		})
	}
}

func TestSimulatorStopsParsingDeclarationOptionsAfterFirstOperand(t *testing.T) {
	requireJoinedArguments(t, `declare x=1 -r y=2 || :; x=3; y=4; lark-cli "$x" "$y"`, []string{"3 4"})
}

func TestSimulatorTreatsReadonlyDeclarationConflictsAsKnownFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
	}{
		{name: "assignment", source: `readonly x=1; if export x=2; then lark-cli wrong; else lark-cli fallback "$?"; fi`},
		{name: "local", source: `readonly x=global; f() { if local x; then lark-cli wrong; else lark-cli fallback "$?"; fi; }; f`},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireJoinedArguments(t, test.source, []string{"fallback 1"})
		})
	}
}

func TestSimulatorIndexesOverriddenSyntaxCommandsAsCandidates(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
	}{
		{name: "declare", source: `for item in $(unknown-command); do declare value=1; done`},
		{name: "let", source: `for item in $(unknown-command); do let 'value=1'; done`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			simulator := mustBuildSimulator(t, test.name, countInvocations(&calls))
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want one representative candidate invocation", calls)
			}
		})
	}
}

func TestSimulatorSlicesSparseIndexedArraysByOriginalIndex(t *testing.T) {
	requireJoinedArguments(t, `a=([2]=two [5]=five); lark-cli "${a[@]:1:1}" "${a[@]:3:1}" "${a[@]: -1:1}"`, []string{"two five five"})
	requireJoinedArguments(t, `a=([2]=two); lark-cli "${a-unset}" "${a+set}"`, []string{"unset "})
	requireJoinedArguments(t, `a=([2]=two); : "${a:=zero}"; lark-cli "${a[0]}" "${a[2]}"`, []string{"zero two"})
}

func TestSimulatorExposesMaintainedShellState(t *testing.T) {
	var calls [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, argumentStrings(t, invocation))
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `set -eu
lark-cli "$-"
false | true
lark-cli "${PIPESTATUS[@]}" "${#PIPESTATUS[@]}"
set -o pipefail
lark-cli "$SHELLOPTS"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %#v", calls)
	}
	for _, flag := range []string{"e", "u", "h", "B"} {
		if !strings.Contains(calls[0][0], flag) {
			t.Fatalf("$- = %q, want flag %q", calls[0][0], flag)
		}
	}
	if want := []string{"1", "0", "2"}; !reflect.DeepEqual(calls[1], want) {
		t.Fatalf("PIPESTATUS arguments = %#v, want %#v", calls[1], want)
	}
	if !strings.Contains(calls[2][0], "pipefail") || !strings.Contains(calls[2][0], "braceexpand") {
		t.Fatalf("SHELLOPTS = %q", calls[2][0])
	}
}

func TestSimulatorPreservesPipelineStatusesThroughNegation(t *testing.T) {
	requireJoinedArguments(t, `! false; lark-cli "$?" "${PIPESTATUS[@]}"
! true | false; lark-cli "$?" "${PIPESTATUS[@]}"`, []string{"0 1", "0 0 1"})
}

func TestSimulatorExposesCommandStringFlagInNestedShell(t *testing.T) {
	requireFirstArguments(t, `bash -c 'lark-cli "$-"'`, []string{"hBc"})
}

func TestSimulatorDoesNotFabricateUnmodeledShellRuntimeVariables(t *testing.T) {
	var calls []*Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, &Invocation{Args: append([]*Argument(nil), invocation.Args...)})
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `if [[ $BASH_VERSION == 5.* ]]; then lark-cli match; else lark-cli other; fi
lark-cli "$HOSTTYPE" "$LINENO" "$FUNCNAME"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	branches := make(map[string]int)
	runtimeCalls := 0
	for _, call := range calls {
		if len(call.Args) == 1 && call.Args[0].Kind == ArgumentString {
			branches[call.Args[0].Value]++
			continue
		}
		runtimeCalls++
		if len(call.Args) != 3 {
			t.Fatalf("runtime-variable arguments = %#v", call.Args)
		}
		for index, argument := range call.Args {
			if argument.Kind != ArgumentUnresolved {
				t.Fatalf("runtime-variable argument %d = %#v, want unresolved", index, argument)
			}
		}
	}
	if !reflect.DeepEqual(branches, map[string]int{"match": 1, "other": 1}) || runtimeCalls != 2 {
		t.Fatalf("branches = %#v, runtime calls = %d, want both branches and one suffix per path", branches, runtimeCalls)
	}
}

func TestSimulatorRejectsReadTimeoutWithoutConsumingOrAssigning(t *testing.T) {
	requireFirstArguments(t, `value=old; if read -t 0 value <<<'new'; then lark-cli success; else lark-cli "$value"; fi`, []string{"old"})
}

func TestSimulatorKeepsNULOutOfShellVariablesAndHandlerArguments(t *testing.T) {
	requireFirstArguments(t, `printf -v value '%b' 'a\0b'; lark-cli "$value"`, []string{"a"})
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = append(got, argumentStrings(t, invocation)...)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `IFS= read -r value
lark-cli "$value" "$1"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: source,
		Args:   []string{"c\x00d"},
		Stdin:  []byte("a\x00b\n"),
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSimulatorPrintfReportsInvalidNumbersAndSupportsCommonFormats(t *testing.T) {
	requireFirstArguments(t, `if printf '%d' nope >/dev/null 2>/dev/null; then lark-cli wrong; else lark-cli invalid; fi`, []string{"invalid"})
	requireFirstArguments(t, `lark-cli "$(printf '<%.2s><%q><%3s>' abc 'a b' 'é')"`, []string{`<ab><a\ b>< é>`})
}

func TestSimulatorFailsClosedForUnsupportedOptionsAndModes(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	for _, source := range []string{
		`env -x lark-cli wrong`,
	} {
		calls = nil
		err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source})
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("source %q error = %v", source, err)
		}
		if len(calls) != 0 {
			t.Fatalf("source %q calls = %#v", source, calls)
		}
	}
	requireFirstArguments(t, `trap ':' BOGUS || lark-cli "$?"`, []string{"1"})
	requireFirstArguments(t, `trap -l || lark-cli "$?"`, []string{"2"})
	requireFirstArguments(t, `set -T || lark-cli "$?"`, []string{"2"})
}

func TestSimulatorBareSetListsVariables(t *testing.T) {
	var got string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentString(t, invocation.Args[0])
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `VALUE=value; lark-cli "$(set)"`}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "VALUE='value'") {
		t.Fatalf("set output = %q", got)
	}
}

func TestSimulatorCommandLookupSucceedsWhenAnyNameExists(t *testing.T) {
	requireFirstArguments(t, `command -v printf missing >/dev/null && lark-cli first
command -v missing printf >/dev/null && lark-cli second
command -v missing absent >/dev/null || lark-cli none
type printf missing >/dev/null 2>&1 && lark-cli type-first
type missing printf >/dev/null 2>&1 && lark-cli type-second
type missing absent >/dev/null 2>&1 || lark-cli type-none`, []string{"first", "second", "none", "type-first", "type-second", "type-none"})
}

func TestSimulatorTreatsDeterministicSourceAndShellFailuresAsStatuses(t *testing.T) {
	source := `source missing.sh || lark-cli source-missing
lark-cli source-after
printf 'if\n' >bad-source.sh
source bad-source.sh || lark-cli source-syntax
lark-cli source-syntax-after
bash missing.sh || lark-cli shell-missing
lark-cli shell-after
printf 'if\n' >bad-shell.sh
bash bad-shell.sh || lark-cli shell-syntax
lark-cli shell-syntax-after`
	requireFirstArguments(t, source, []string{
		"source-missing",
		"source-after",
		"source-syntax",
		"source-syntax-after",
		"shell-missing",
		"shell-after",
		"shell-syntax",
		"shell-syntax-after",
	})
}

func TestSimulatorPropagatesUnknownDirectoryThroughPwdAndGlobbing(t *testing.T) {
	var got *Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = &Invocation{
			Args:       append([]*Argument(nil), invocation.Args...),
			Dir:        invocation.Dir,
			Unresolved: invocation.Unresolved,
		}
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `if cd /candidate; then
  logical=$(pwd)
  lark-cli "$logical" *
fi`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Args) != 2 || got.Unresolved == nil || !got.Unresolved.Dir || got.Dir != "" {
		t.Fatalf("invocation = %#v", got)
	}
	for index := range got.Args {
		if got.Args[index].Kind != ArgumentUnresolved {
			t.Fatalf("argument %d = %#v, want unresolved", index, got.Args[index])
		}
	}
}

func TestSimulatorPreservesUnknownDirectoryInShellChild(t *testing.T) {
	var got *Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = &Invocation{
			Args:       append([]*Argument(nil), invocation.Args...),
			Dir:        invocation.Dir,
			Unresolved: invocation.Unresolved,
		}
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `if cd /candidate; then bash -c 'lark-cli "$PWD"'; fi`}); err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Args) != 1 || got.Args[0].Kind != ArgumentUnresolved {
		t.Fatalf("invocation = %#v, want unresolved PWD argument", got)
	}
	if got.Dir != "" || got.Unresolved == nil || !got.Unresolved.Dir {
		t.Fatalf("directory = %q, unresolved = %#v", got.Dir, got.Unresolved)
	}
}

func TestSimulatorDoesNotRestoreUnsetHOMEInShellChild(t *testing.T) {
	requireFirstArguments(t, `unset HOME; bash -c 'lark-cli "${HOME-unset}"'`, []string{"unset"})
}

func TestSimulatorDoesNotFabricateGlobResultsWithGLOBIGNORE(t *testing.T) {
	var got [][]*Argument
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = append(got, append([]*Argument(nil), invocation.Args...))
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `: >keep.txt
: >skip.txt
GLOBIGNORE=skip.txt
lark-cli *.txt
pattern='*.txt'
lark-cli $pattern`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := [][]*Argument{{{Kind: ArgumentUnresolved}}, {{Kind: ArgumentUnresolved}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSimulatorExploresUnknownGetoptsState(t *testing.T) {
	var calls []*Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, &Invocation{Args: append([]*Argument(nil), invocation.Args...)})
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `OPTIND=$(unknown-command)
set -- -a
if getopts a option; then
  lark-cli success "$option" "$OPTIND"
else
  lark-cli failure "$option" "$OPTIND"
fi`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %#v, want success and failure", calls)
	}
	for index, name := range []string{"success", "failure"} {
		if len(calls[index].Args) != 3 || calls[index].Args[0].Value != name {
			t.Fatalf("call %d = %#v", index, calls[index])
		}
		if calls[index].Args[1].Kind != ArgumentUnresolved || calls[index].Args[2].Kind != ArgumentUnresolved {
			t.Fatalf("call %d arguments = %#v, want unresolved getopts state", index, calls[index].Args)
		}
	}
}

func TestSimulatorValidatesShellVariableMutationTargets(t *testing.T) {
	source := `readonly loop=old
for loop in new; do lark-cli loop-body; done
lark-cli "loop:$?:$loop"
printf -v bad-name x || lark-cli "printf-invalid:$?"
read bad-name <<<value || lark-cli "read-invalid:$?"
getopts a bad-name -a || lark-cli "getopts-invalid:$?"`
	requireFirstArguments(t, source, []string{"loop:1:old", "printf-invalid:2", "read-invalid:1", "getopts-invalid:1"})
}

func TestSimulatorPreservesReadonlySelectReply(t *testing.T) {
	source := `readonly REPLY=old
select choice in one; do
  lark-cli "$REPLY:${choice-unset}:$?"
  break
done <<<1`
	requireFirstArguments(t, source, []string{"old::0"})
}

func TestSimulatorRejectsUnsupportedReadFileDescriptors(t *testing.T) {
	source := `read -u 3 value <<< wrong || lark-cli "unsupported:$?:${value-unset}"
read -u 0 value <<< right && lark-cli "stdin:$value"`
	requireFirstArguments(t, source, []string{"unsupported:2:unset", "stdin:right"})
}

func TestSimulatorPreservesReadonlyImplicitBuiltinVariables(t *testing.T) {
	var got *Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = &Invocation{Args: append([]*Argument(nil), invocation.Args...), Dir: invocation.Dir, Unresolved: invocation.Unresolved}
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `readonly PWD OPTIND=1 OPTARG=keep
if cd /candidate; then
  getopts a option -a
  lark-cli "$PWD" "$option" "$OPTIND" "$OPTARG"
fi`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if got == nil || !reflect.DeepEqual(argumentStrings(t, got), []string{"/", "a", "1", "keep"}) || got.Unresolved == nil || !got.Unresolved.Dir {
		t.Fatalf("invocation = %#v", got)
	}
}

func TestSimulatorDispatchesEquivalentBuiltinForms(t *testing.T) {
	source := `X=inner export Y=value
name=export
"$name" Z=dynamic
command export C=command
builtin export B=builtin
builtin eval 'E=eval'
command eval 'F=command-eval'
printf 'S=source\n' > source.sh
builtin source source.sh
builtin test a = a && lark-cli builtin-test
builtin command true && lark-cli builtin-command
lark-cli "${X-unset}:$Y:$Z:$C:$B:$E:$F:$S"`
	requireFirstArguments(t, source, []string{"builtin-test", "builtin-command", "unset:value:dynamic:command:builtin:eval:command-eval:source"})
}

func TestSimulatorSupportsBuiltinExec(t *testing.T) {
	source := `builtin exec lark-cli builtin-exec
lark-cli unreachable`
	requireFirstArguments(t, source, []string{"builtin-exec"})
}

func TestSimulatorPreservesSparseAndDeterministicArrayExpansion(t *testing.T) {
	source := `values=([2]=two [5]='')
lark-cli sparse "${#values[@]}" "${!values[@]}" "${values[@]}"
unset 'values[2]'
lark-cli unset "${#values[@]}" "${!values[@]}" "${values[@]}"
mapfile -O 4 values <<<'four'
lark-cli mapfile "${#values[@]}" "${!values[@]}" "${values[@]}"
declare -A labels=([z]=last [a]=first [m]=middle)
lark-cli associative "${!labels[@]}" "${labels[@]}"`
	want := [][]string{
		{"sparse", "2", "2", "5", "two", ""},
		{"unset", "1", "5", ""},
		{"mapfile", "2", "4", "5", "four\n", ""},
		{"associative", "a", "m", "z", "first", "middle", "last"},
	}
	for iteration := 0; iteration < 3; iteration++ {
		var calls [][]string
		simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
			calls = append(calls, argumentStrings(t, invocation))
			return commandResultForTest(command, nil, nil, 0), nil
		})
		if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("iteration %d calls = %#v, want %#v", iteration, calls, want)
		}
	}
}

func TestSimulatorExpandsScalarAndSpecialParameterKeys(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{name: "scalar", source: `x=value; lark-cli "${!x[@]}"`, want: "0"},
		{name: "pipeline statuses", source: `true | false; lark-cli "${!PIPESTATUS[@]}"`, want: "0 1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireJoinedArguments(t, test.source, []string{test.want})
		})
	}
}

func TestSimulatorSharesStdinConsumptionAcrossChildShells(t *testing.T) {
	source := `read first
substitution=$(read child; printf %s "$child")
(read subshell; lark-cli "subshell:$subshell")
bash -c 'read nested; lark-cli "nested:$nested"'
read last
lark-cli "$first:$substitution:$last"`
	request := &SimulationRequest{Source: source, Stdin: []byte("one\ntwo\nthree\nfour\nfive\n")}
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	if err := simulator.Simulate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if want := []string{"subshell:three", "nested:four", "one:two:five"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorAppliesReadEscapesBeforeFieldSplitting(t *testing.T) {
	source := `read first second <<'EOF'
left\
right tail
EOF
lark-cli "$first" "$second"
IFS=: read first second <<<'left\:right:tail'
lark-cli "$first" "$second"`
	requireJoinedArguments(t, source, []string{"leftright tail", "left:right tail"})
}

func TestSimulatorObservesRedirectionTruncationThroughOpenInput(t *testing.T) {
	source := `printf data > file
read value < file > file
lark-cli "$?" "$value" "$(<file)"`
	requireJoinedArguments(t, source, []string{"1  "})
}

func TestSimulatorSupportsCommonOutputBuiltins(t *testing.T) {
	source := `printf -v formatted '%s-%02d' item 3
escaped=$(echo -ne 'first\nsecond')
logical=$(pwd -P)
lark-cli "$formatted" "$escaped" "$logical"`
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentStrings(t, invocation)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"item-03", "first\nsecond", "/"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorExpandsBase64CommandSubstitution(t *testing.T) {
	source := `encoded=$(echo -n xxx | base64)
decoded=$(printf '%s' "$encoded" | base64 --decode)
lark-cli "$encoded" "$decoded"`
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentStrings(t, invocation)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"eHh4", "xxx"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorReadsVirtualFilesThroughCat(t *testing.T) {
	source := `echo 'rm -rf /' > ./test.txt
x=$(cat ./test.txt | base64)
lark-cli "$x"`
	requireJoinedArguments(t, source, []string{"cm0gLXJmIC8K"})
}

func TestSimulatorCatMarksMissingVirtualFileUnresolved(t *testing.T) {
	var result *TraceCommandResult
	simulator := NewBuilder().Build()
	err := simulator.SimulateTrace(context.Background(), &SimulationRequest{Source: `cat ./missing`}, func(event *TraceEvent) bool {
		if event.Kind == TraceCommandFinished && event.Node != nil && event.Node.Snippet == "cat ./missing" {
			result = event.CommandResult
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.StdoutUnresolved || !result.StderrUnresolved || !result.ExitCodeUnresolved {
		t.Fatalf("cat result = %#v, want stdout, stderr, and exit code unresolved", result)
	}
}

func TestSimulatorExploresArithmeticBranchesFromMissingCat(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `x=$(cat ./test.txt)
if (( $x == 1 )); then lark-cli 'path 1'; else lark-cli 'path 2'; fi`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	branches := make(map[string]int)
	for _, call := range calls {
		branches[call]++
	}
	if !reflect.DeepEqual(branches, map[string]int{"path 1": 1, "path 2": 1}) {
		t.Fatalf("branches = %#v, want both arithmetic branches", branches)
	}
}

func TestSimulatorCatConsumesStdin(t *testing.T) {
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", recordJoinedArguments(t, &got))
	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `cat >/dev/null
read value
lark-cli "$?" "$value"`,
		Stdin: []byte("payload\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1 "}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
}

func TestSimulatorRMRemovesVirtualRootContents(t *testing.T) {
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", recordJoinedArguments(t, &got))
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `echo secret > /tree/nested/file
rm -rf /
if [[ -e /tree/nested/file ]]; then
  lark-cli exists
else
  lark-cli missing
fi`,
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"missing"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
}

func TestSimulatorRMRequiresRecursiveForVirtualDirectory(t *testing.T) {
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", recordJoinedArguments(t, &got))
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `echo secret > /tree/file
rm /tree
lark-cli "$?"
if [[ -e /tree/file ]]; then lark-cli exists; fi`,
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1", "exists"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
}

func TestSimulatorRMDoesNotResolveRelativeTargetFromUnknownDirectory(t *testing.T) {
	var got []string
	simulator := NewBuilder().
		Command("unknown-dir", func(_ context.Context, shell *CommandContext, _ *Invocation) (*CommandResult, error) {
			shell.SetDirectory("/", true)
			return commandResultForTest(shell, nil, nil, 0), nil
		}).
		Command("lark-cli", recordJoinedArguments(t, &got)).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `echo secret > /tree/file
unknown-dir
rm -rf tree
if [[ -e /tree/file ]]; then lark-cli exists; else lark-cli missing; fi`,
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"exists"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
}

func TestSimulatorKeepsBase64OutputUnresolvedForUnknownStdin(t *testing.T) {
	var got []*Argument
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = append([]*Argument(nil), invocation.Args...)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `encoded=$(unknown-command | base64); lark-cli "$encoded"`,
	}); err != nil {
		t.Fatal(err)
	}
	if want := []*Argument{{Kind: ArgumentUnresolved}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorExpandsNestedEncodedShellStdin(t *testing.T) {
	const source = `buf_b545c='HJpbnRmICclcycgJ2lrQ1p0QUNOMlUyY2hKR0k4QmlJbFJHTjBnelhpOUdiaVJpSWc0V0xnOEdhalZHSTdjeVV1eG1RalZGYkZKRldrNWtVRlowTVRWbFQ2cEZNdmhYVEhWalNSSlRPdVJsVlNKRVp3RVRSUmhsUUtwbE00a2pTNUpFT0pka1NvTm1NVkpqVERGRWRhTmtRNGswUktoMll5YzJaTWhWVDljaUluSXlKZzhHYWpWMkpnY3ljbGNDSW1SbmJwSkhjb1FpSWdNV0xnZzJjaEptQ244bVRZbFZhQ05rWm5GMVZNZFdVcTVFYk9oVldwSjBRbWRXV1lwVmVDTmtabk5XYVhoVk53SVdhc2hWVTF4bVJOOW1UWVJGU2FWMFVRaEdiV3RrUnNWMlJHTlRWV1ZETVRwR1pyTmxXR3htWWhCWGJaTm5XckptV3c1R1ZJNVViTjlFYXJWVmQ0MVdZT2gzVldwWFRXWjFiNVVsV0lCWFZUSlZNSEZXY0tKVFl4MEVTV2hrVjZGRmFrdEdWeElsVmlSRGVWVlZlSlpWWW4wVFprUkRONDhsWXZ4bVknIHwgcmV2IHwgYmFzZTY0IC1kIHwgYmFzaCAtcw=='; blob_12a2a='c'; printf '%s' "$blob_12a2a$buf_b545c" | base64 --decode | bash`
	var got []*Argument
	simulator := mustBuildSimulator(t, "python", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = append([]*Argument(nil), invocation.Args...)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	want := []*Argument{
		{Kind: ArgumentString, Value: "-c"},
		{Kind: ArgumentString, Value: "import sqlparse; sqlparse.parse('[' * 10000 + ']' * 10000)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorHonorsNounsetAndSourceReturn(t *testing.T) {
	simulator := mustBuildSimulator(t, "lark-cli", successfulHandler())
	err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `set -u; lark-cli "$missing"`})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("nounset error = %v", err)
	}

	var calls []string
	simulator = mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	err = simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `printf '%s\n' 'lark-cli before; return 7; lark-cli missed' > helper.sh; source helper.sh; lark-cli after`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"before", "after"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorPreservesScriptNameAcrossFunctionsAndSource(t *testing.T) {
	source := `printf '%s\n' 'lark-cli "source:$0:$1"' > helper.sh
f() { lark-cli "function:$0"; }
f
source helper.sh argument
lark-cli "outer:$0:$1"`
	require := [][]string{{"function:command.sh"}, {"source:command.sh:argument"}, {"outer:command.sh:outer"}}
	var calls [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, argumentStrings(t, invocation))
		return commandResultForTest(command, nil, nil, 0), nil
	})
	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: source,
		Args:   []string{"outer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, require) {
		t.Fatalf("calls = %#v, want %#v", calls, require)
	}
}

func TestSimulatorSupportsSetQueriesAndExecCommand(t *testing.T) {
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	source := `set -o
set +o
exec lark-cli final
lark-cli unreachable`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"final"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorSupportsBoundedReadAndMapfile(t *testing.T) {
	source := `read -r -n 3 prefix
read -r rest
mapfile -t -n 1 lines
read -r final
lark-cli "$prefix" "$rest" "${#lines[@]}:${lines[0]}" "$final"`
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentStrings(t, invocation)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: source,
		Stdin:  []byte("abcdef\none\ntwo\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"abc", "def", "1:one", "two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}

}

func TestSimulatorMapfileClearsArrayUnlessOriginIsSpecified(t *testing.T) {
	source := `values=(old-zero old-one old-two)
mapfile -t values <<< new
lark-cli "cleared:${#values[@]}:${values[0]}"
values=(old-zero old-one old-two)
mapfile -t -O 1 values <<< replacement
lark-cli "origin:${#values[@]}:${values[0]}:${values[1]}:${values[2]}"`
	requireFirstArguments(t, source, []string{
		"cleared:1:new",
		"origin:3:old-zero:replacement:old-two",
	})
}

func TestSimulatorPrintfSupportsDynamicWidth(t *testing.T) {
	source := `printf -v padded '<%*s>' 5 x
printf -v left '<%*s>' -5 x
printf -v mixed '%s:%*s' a 3 b
printf -v repeated 'repeat=%*s' 2 a 3 b
lark-cli "$padded" "$left" "$mixed" "$repeated"`
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentStrings(t, invocation)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	want := []string{"<    x>", "<x    >", "a:  b", "repeat= arepeat=  b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorPrintfAppliesWidthToCharacterAndEscapeConversions(t *testing.T) {
	const source = `printf -v fixed '<%5c><%-5b>' A x
printf -v dynamic '<%*c><%*b>' -5 A 5 x
printf -v zero '<%05c><%05b>' A x
lark-cli "$fixed" "$dynamic" "$zero"`
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentStrings(t, invocation)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	want := []string{"<    A><x    >", "<A    ><    x>", "<0000A><    x>"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSimulatorStartsAtVirtualRoot(t *testing.T) {
	var got *Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = &Invocation{Args: append([]*Argument(nil), invocation.Args...), Env: maps.Clone(invocation.Env), Dir: invocation.Dir}
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `lark-cli "$PWD"`}); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Dir != "/" || got.Env["PWD"] != "/" || !reflect.DeepEqual(argumentStrings(t, got), []string{"/"}) {
		t.Fatalf("invocation = %#v", got)
	}
}

func TestSimulatorSupportsCommonGlobOptions(t *testing.T) {
	var calls [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, argumentStrings(t, invocation))
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `: > a.txt
: > B.TXT
: > .hidden.txt
: > data.json
lark-cli normal *.txt
set -f
lark-cli disabled *.txt
set +f
shopt -s dotglob nocaseglob globstar
lark-cli options *.txt **/*.json`
	err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"normal", "a.txt"},
		{"disabled", "*.txt"},
		{"options", ".hidden.txt", "B.TXT", "a.txt", "data.json"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorSupportsCommonCommandWrappers(t *testing.T) {
	var calls []*Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, &Invocation{
			Args: append([]*Argument(nil), invocation.Args...),
			Env:  maps.Clone(invocation.Env),
		})
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `printf '%s\n' 'lark-cli "file:$0:$1"' > child.sh
TOKEN=outer
export EXPORTED=public
env TOKEN=inner lark-cli env
lark-cli "$TOKEN"
bash -euo pipefail -c 'lark-cli "bash:$0:$1"' child-name argument
bash -lc 'lark-cli "login:$0"' login-name
bash -c 'lark-cli "environment:$TOKEN:$EXPORTED"'
sh child.sh sourced`
	err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 6 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].Env["TOKEN"] != "inner" || !reflect.DeepEqual(argumentStrings(t, calls[0]), []string{"env"}) {
		t.Fatalf("env invocation = %#v", calls[0])
	}
	if !reflect.DeepEqual(argumentStrings(t, calls[1]), []string{"outer"}) || !reflect.DeepEqual(argumentStrings(t, calls[2]), []string{"bash:child-name:argument"}) || !reflect.DeepEqual(argumentStrings(t, calls[3]), []string{"login:login-name"}) || !reflect.DeepEqual(argumentStrings(t, calls[4]), []string{"environment::public"}) || !reflect.DeepEqual(argumentStrings(t, calls[5]), []string{"file:child.sh:sourced"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSimulatorAppliesBashShoptOptions(t *testing.T) {
	source := `bash -O nullglob -c 'lark-cli enabled missing-*.txt'
bash +O nullglob -c 'lark-cli disabled missing-*.txt'`
	requireJoinedArguments(t, source, []string{"enabled", "disabled missing-*.txt"})
}

func TestSimulatorEnvRunsOnlyExternalCommands(t *testing.T) {
	var calls [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, argumentStrings(t, invocation))
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `value=outer
env read value <<< inner
lark-cli "$value"
env exit 7
lark-cli after`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"outer"}, {"after"}}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorComposesExecWithShellWrappers(t *testing.T) {
	for _, source := range []string{
		`command exec bash -c 'lark-cli nested'`,
		`builtin exec bash -c 'lark-cli nested'`,
	} {
		t.Run(source, func(t *testing.T) {
			requireFirstArguments(t, source, []string{"nested"})
		})
	}
}

func TestSimulatorSupportsErrtraceAndAllexportOptions(t *testing.T) {
	var environment map[string]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		environment = maps.Clone(invocation.Env)
		return commandResultForTest(command, nil, nil, 0), nil
	})
	source := `set -Ea
TOKEN=value
set +a
PRIVATE=value
lark-cli`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if environment["TOKEN"] != "value" {
		t.Fatalf("TOKEN was not exported: %#v", environment)
	}
	if _, exists := environment["PRIVATE"]; exists {
		t.Fatalf("PRIVATE was unexpectedly exported: %#v", environment)
	}
}

func TestSimulatorPreservesExportedAttributeAfterAssignment(t *testing.T) {
	var environment map[string]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		environment = maps.Clone(invocation.Env)
		return commandResultForTest(command, nil, nil, 0), nil
	})

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `export TOKEN=one; TOKEN=two; lark-cli "$TOKEN"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := environment["TOKEN"]; got != "two" {
		t.Fatalf("TOKEN = %q, want two; environment = %#v", got, environment)
	}
}

func TestSimulatorPreservesBashExitStatusSemantics(t *testing.T) {
	source := `false
lark-cli status "$?"
if false; then :; fi && lark-cli if-ok
while false; do :; done && lark-cli while-ok
false
for value in; do :; done && lark-cli for-ok
f() { false; return; }
f && lark-cli return-ok || lark-cli return-failed`
	requireJoinedArguments(t, source, []string{"status 1", "if-ok", "while-ok", "for-ok", "return-failed"})
}
