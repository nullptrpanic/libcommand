package libcommand

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestKnownBodiesAcceptUnknownPositionalArguments(t *testing.T) {
	for _, test := range []*struct{ name, source string }{
		{"function", `f(){ record "$#" "$1" "$2" "$3"; }; f known "$(missing)" tail`},
		{"shell", `bash -c 'record "$#" "$1" "$2" "$3"' -- known "$(missing)" tail`},
		{"source", `echo 'record "$#" "$1" "$2" "$3"' > body; source ./body known "$(missing)" tail`},
		{"file shell", `echo 'record "$#" "$1" "$2" "$3"' > body; bash ./body known "$(missing)" tail`},
		{"stdin shell", `bash -s -- known "$(missing)" tail <<'BODY'
record "$#" "$1" "$2" "$3"
BODY
`},
		{"forward function", `f(){ g "$@"; }; g(){ record "$#" "$1" "$2" "$3"; }; f known "$(missing)" tail`},
		{"shift preserves unknown", `f(){ shift; record "$#" "$1" "$2" "$3"; }; f removed known "$(missing)" tail`},
		{"set preserves unknown", `f(){ set -- "$@"; record "$#" "$1" "$2" "$3"; }; f known "$(missing)" tail`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls [][]string
			simulator := NewBuilder().Command("record", func(_ context.Context, shell *CommandContext, invocation *Invocation) (*CommandResult, error) {
				var values []string
				for _, arg := range invocation.Args {
					values = append(values, fmt.Sprintf("%d:%s", arg.Kind, arg.Value))
				}
				calls = append(calls, values)
				return shell.Result(shell.Output().Build()), nil
			}).Build()
			err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `set -- original; ` + test.source + "\n" + `record "$1"`})
			want := [][]string{{"0:3", "0:known", "1:", "0:tail"}, {"0:original"}}
			if err != nil || !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%q, error=%v; want %q", calls, err, want)
			}
		})
	}
}

func TestUnknownPositionalCountIsKnownAndEmptyIsDistinct(t *testing.T) {
	requireFirstArguments(t, `f(){ record="$#:${#@}:${#*}:<$1>"; lark-cli "$record"; }; f '' "$(missing)"`, []string{"2:2:2:<>"})
}

func TestUnknownPositionalsRetainOrder(t *testing.T) {
	for _, test := range []*struct {
		source string
		want   [][]string
	}{
		{`f(){ for value in "$@"; do record "$value"; done; }; f known "$(missing)" tail`, [][]string{{"0:known"}, {"1:"}, {"0:tail"}}},
		{`f(){ getopts ab: opt; record "$opt" "$OPTIND"; getopts ab: opt; record "$opt" "$OPTARG" "$OPTIND"; }; f -a -b "$(missing)"`, [][]string{{"0:a", "0:2"}, {"0:b", "1:", "0:4"}}},
		{`f(){ if getopts a opt; then record yes "$opt"; else record no "$opt"; fi; }; f "$(missing)"`, [][]string{{"0:yes", "1:"}, {"0:no", "1:"}}},
	} {
		t.Run(test.source, func(t *testing.T) {
			var calls [][]string
			simulator := NewBuilder().Command("record", func(_ context.Context, shell *CommandContext, invocation *Invocation) (*CommandResult, error) {
				values := make([]string, len(invocation.Args))
				for i, arg := range invocation.Args {
					values[i] = fmt.Sprintf("%d:%s", arg.Kind, arg.Value)
				}
				calls = append(calls, values)
				return shell.Result(shell.Output().Build()), nil
			}).Build()
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("calls=%q, want %q", calls, test.want)
			}
		})
	}
}

func TestCallerCommandCanForwardTypedShellArguments(t *testing.T) {
	var calls [][]string
	simulator := NewBuilder().
		Command("wrapper", func(_ context.Context, shell *CommandContext, invocation *Invocation) (*CommandResult, error) {
			return shell.RunShell(&ShellProgram{Source: `capture "$1" "$2"`}).WithArguments(invocation.Args), nil
		}).
		Command("replace", func(_ context.Context, shell *CommandContext, invocation *Invocation) (*CommandResult, error) {
			return shell.Replace("capture", invocation.Args, false).WithArgv0("custom-name"), nil
		}).
		Command("capture", func(_ context.Context, shell *CommandContext, invocation *Invocation) (*CommandResult, error) {
			values := []string{invocation.Name, shell.Argv0()}
			for _, arg := range invocation.Args {
				values = append(values, fmt.Sprintf("%d:%s", arg.Kind, arg.Value))
			}
			calls = append(calls, values)
			return shell.Result(shell.Output().Build()), nil
		}).Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `wrapper known "$(missing)"; replace final; capture unreachable`}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"capture", "capture", "0:known", "1:"}, {"capture", "custom-name", "0:final"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%q, want %q", calls, want)
	}
}
