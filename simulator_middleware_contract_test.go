package libcommand

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestMiddlewareObservesPreparedSyntaxCommands(t *testing.T) {
	for _, test := range []*struct {
		name, source, result string
		args                 []string
		once                 int
	}{
		{"declare", `declare x="$TOKEN"; lark-cli "$x"`, "visible", []string{"0:x=visible"}, 0},
		{"let", `let 'x=2'; lark-cli "$x"`, "2", []string{"0:x=2"}, 0},
		{"let", `let x=2; lark-cli "$x"`, "2", []string{"0:x=2"}, 0},
		{"let", `i=0; let x=$((i++)) y=i+1; lark-cli "$x:$y:$i"`, "0:2:1", []string{"0:x=0", "0:y=i+1"}, 0},
		{"let", `x=1; let x++; lark-cli "$x"`, "2", []string{"0:x++"}, 0},
		{"declare", `i=0; declare x=$((i++)) y=$i; lark-cli "$x:$y:$i"`, "0:1:1", []string{"0:x=0", "0:y=1"}, 0},
		{"let", `i=0; let "x=$((i++))"; lark-cli "$x:$i"`, "0:1", []string{"0:x=0"}, 0},
		{"declare", `declare x="$(once)"; lark-cli "$x"`, "expanded", []string{"0:x=expanded"}, 1},
		{"declare", `i=0; declare -a a=($((i++)) "$TOKEN"); lark-cli "${a[0]}:${a[1]}:$i"`, "0:visible:1", []string{"0:-a", "0:a"}, 0},
		{"declare", `declare -A a=(["$(once)"]="$TOKEN"); lark-cli "${a[expanded]}"`, "visible", []string{"0:-A", "0:a"}, 1},
		{"declare", `i=0; declare a[$((i++))]=value; lark-cli "${a[0]}:$i"`, "value:1", []string{"0:a[0]=value"}, 0},
		{"declare", `x=before; declare x=after y=$x; lark-cli "$x:$y"`, "after:before", []string{"0:x=after", "0:y=before"}, 0},
		{"declare", `entry='x=value'; declare "$entry"; lark-cli "$x"`, "value", []string{"0:x=value"}, 0},
	} {
		for _, observed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/observed=%t", test.source, observed), func(t *testing.T) {
				var calls, arguments []string
				once := 0
				builder := NewBuilder().Command("lark-cli", recordFirstArgument(t, &calls)).Command("once", func(_ context.Context, shell *CommandContext, _ *Invocation) (*CommandResult, error) {
					once++
					return shell.Result(shell.Output().Stdout(Resolved([]byte("expanded"))).Build()), nil
				})
				if observed {
					builder.Middleware(func(next Command) Command {
						return func(ctx context.Context, shell *CommandContext, invocation *Invocation) (*CommandResult, error) {
							if invocation.Env["TOKEN"] != "visible" {
								t.Errorf("%s environment = %v", invocation.Name, invocation.Env)
							}
							if invocation.Name == test.name {
								for _, arg := range invocation.Args {
									arguments = append(arguments, fmt.Sprintf("%d:%s", arg.Kind, arg.Value))
								}
							}
							return next(ctx, shell, invocation)
						}
					})
				}
				if err := builder.Build().Simulate(context.Background(), &SimulationRequest{Source: test.source, Env: map[string]string{"TOKEN": "visible"}}); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(calls, []string{test.result}) || once != test.once {
					t.Fatalf("calls=%q, expansions=%d", calls, once)
				}
				if observed && !reflect.DeepEqual(arguments, test.args) {
					t.Fatalf("arguments=%q, want %q", arguments, test.args)
				}
			})
		}
	}
}

func TestPreparedCommandCanBeShortCircuitedOrOverridden(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(fmt.Sprintf("override=%t", override), func(t *testing.T) {
			var calls []string
			observed, executed := 0, 0
			builder := NewBuilder().Command("lark-cli", recordFirstArgument(t, &calls))
			if override {
				builder.Command("declare", func(_ context.Context, shell *CommandContext, invocation *Invocation) (*CommandResult, error) {
					executed++
					if len(invocation.Args) != 1 || invocation.Args[0].Value != "x=0" {
						t.Fatalf("override args=%v", invocation.Args)
					}
					return shell.Result(shell.Output().Build()), nil
				})
			}
			builder.Middleware(func(next Command) Command {
				return func(ctx context.Context, shell *CommandContext, invocation *Invocation) (*CommandResult, error) {
					if invocation.Name == "declare" {
						observed++
						if len(invocation.Args) != 1 || invocation.Args[0].Value != "x=0" {
							t.Fatalf("middleware args=%v", invocation.Args)
						}
						if !override {
							return shell.Result(shell.Output().Build()), nil
						}
					}
					return next(ctx, shell, invocation)
				}
			})
			if err := builder.Build().Simulate(context.Background(), &SimulationRequest{Source: `i=0; declare x=$((i++)); lark-cli "${x-unset}:$i"`}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, []string{"unset:1"}) || observed != 1 || override && executed != 1 || !override && executed != 0 {
				t.Fatalf("calls=%q, observed=%d, overrides=%d", calls, observed, executed)
			}
		})
	}
}

func TestMiddlewarePreservesUnknownDeclarationArguments(t *testing.T) {
	var observed, called int
	builder := NewBuilder().Command("record", func(_ context.Context, shell *CommandContext, invocation *Invocation) (*CommandResult, error) {
		called++
		if len(invocation.Args) != 2 || invocation.Args[0].Kind != ArgumentUnresolved || invocation.Args[1].Value != "known" {
			t.Fatalf("args=%v", invocation.Args)
		}
		return shell.Result(shell.Output().Build()), nil
	}).Middleware(func(next Command) Command {
		return func(ctx context.Context, shell *CommandContext, invocation *Invocation) (*CommandResult, error) {
			if invocation.Name == "declare" {
				observed++
				if len(invocation.Args) != 2 || invocation.Args[0].Kind != ArgumentUnresolved || invocation.Args[1].Value != "y=known" {
					t.Fatalf("args=%v", invocation.Args)
				}
			}
			return next(ctx, shell, invocation)
		}
	})
	if err := builder.Build().Simulate(context.Background(), &SimulationRequest{Source: `declare x="$(missing)" y=known; record "$x" "$y"`}); err != nil {
		t.Fatal(err)
	}
	if observed != 1 || called != 1 {
		t.Fatalf("observed=%d, called=%d", observed, called)
	}
}

func TestPreparedCommandArgumentsRespectAggregateBudget(t *testing.T) {
	for _, command := range []string{"declare", "let"} {
		t.Run(command, func(t *testing.T) {
			calls := 0
			builder := NewBuilder().Limits(&Limits{MaxMemoryBytes: 4096}).Middleware(func(next Command) Command {
				return func(ctx context.Context, shell *CommandContext, invocation *Invocation) (*CommandResult, error) {
					calls++
					return next(ctx, shell, invocation)
				}
			})
			err := builder.Build().Simulate(context.Background(), &SimulationRequest{Source: command + ` a="$big" b="$big" c="$big" d="$big" e="$big"`, Env: map[string]string{"big": strings.Repeat("1", 1000)}})
			if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 4096 reached") || calls != 0 {
				t.Fatalf("error=%v, middleware calls=%d", err, calls)
			}
		})
	}
}
