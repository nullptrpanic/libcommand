package libcommand

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
)

func TestUserCommandEvaluatesShell(t *testing.T) {
	calls := make(map[string]int)
	simulator := NewBuilder().
		Command("evaluate", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			return command.Evaluate(`if unknown-command; then record inner-yes; else record inner-no; fi`, "custom", 1), nil
		}).
		Command("record", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			calls[argumentString(t, invocation.Args[0])]++
			return &CommandResult{}, nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `evaluate; record outer`}); err != nil {
		t.Fatal(err)
	}
	if calls["inner-yes"] != 1 || calls["inner-no"] != 1 || calls["outer"] != 2 || len(calls) != 3 {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestOrdinaryUserCommandDoesNotExposeParserSpecialSyntax(t *testing.T) {
	var commandSyntax any
	simulator := NewBuilder().Command("record",
		func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			commandSyntax = command.CommandSyntax()
			return &CommandResult{}, nil
		}).Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: "record"}); err != nil {
		t.Fatal(err)
	}
	if commandSyntax != nil {
		t.Fatalf("ordinary command syntax = %T, want nil", commandSyntax)
	}
}

func TestUserCommandSourcesShell(t *testing.T) {
	var calls []string
	simulator := NewBuilder().
		Command("source-inline", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			return command.Source(`VALUE=from-source; record "$1:$VALUE"`, "inline", []string{"argument"}), nil
		}).
		Command("record", recordFirstArgument(t, &calls)).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `source-inline; record "$VALUE:${1-unset}"`}); err != nil {
		t.Fatal(err)
	}
	want := []string{"argument:from-source", "from-source:unset"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestUserCommandRunsChildShell(t *testing.T) {
	var calls []string
	simulator := NewBuilder().
		Command("run-child", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			return command.RunShell(&ShellProgram{
				Source:    `VALUE=child; record "$1:$VALUE"`,
				Name:      "child",
				Arguments: []string{"argument"},
			}), nil
		}).
		Command("record", recordFirstArgument(t, &calls)).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `run-child; record "${VALUE-unset}"`}); err != nil {
		t.Fatal(err)
	}
	want := []string{"argument:child", "unset"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestNestedShellParentsCountTowardMemoryLimit(t *testing.T) {
	var recurse Command = func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		depth, err := strconv.Atoi(argumentString(t, invocation.Args[0]))
		if err != nil {
			return nil, err
		}
		if depth == 0 {
			return &CommandResult{}, nil
		}
		return command.RunShell(&ShellProgram{
			Source: fmt.Sprintf("recurse %d", depth-1),
			Name:   "nested.sh",
		}), nil
	}
	simulator := NewBuilder().
		Limits(&Limits{MaxExecutionSteps: 100, MaxMemoryBytes: 8 << 10}).
		Command("recurse", recurse).
		Build()
	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: "recurse 12",
		Env:    map[string]string{"BALLAST": strings.Repeat("x", 1024)},
	})
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 8192 reached") {
		t.Fatalf("nested shell error = %v", err)
	}
}

func TestCommandContextInputDoesNotExposeStateBuffer(t *testing.T) {
	var observed string
	simulator := NewBuilder().
		Command("mutate-copy", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			input, _ := command.Input()
			input[0] = 'X'
			return &CommandResult{}, nil
		}).
		Command("observe-input", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			input, _ := command.Input()
			observed = string(input)
			return &CommandResult{}, nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: "mutate-copy; observe-input", Stdin: []byte("abc")}); err != nil {
		t.Fatal(err)
	}
	if observed != "abc" {
		t.Fatalf("observed input = %q, want abc", observed)
	}
}

func TestCommandContextSetInputCopiesCallerBuffer(t *testing.T) {
	var observed string
	simulator := NewBuilder().
		Command("set-input", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			input := []byte("abc")
			command.SetInput(input, false)
			input[0] = 'X'
			return &CommandResult{}, nil
		}).
		Command("observe-input", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			input, _ := command.Input()
			observed = string(input)
			return &CommandResult{}, nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: "set-input; observe-input"}); err != nil {
		t.Fatal(err)
	}
	if observed != "abc" {
		t.Fatalf("observed input = %q, want abc", observed)
	}
}

func TestCommandContextVariableAssignmentRollsBackOnBudgetFailure(t *testing.T) {
	var mutationErr error
	var exists bool
	simulator := NewBuilder().
		Limits(&Limits{MaxExecutionSteps: 20, MaxMemoryBytes: 512}).
		Command("mutate", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			mutationErr = command.AssignVariable("VALUE", &expand.Variable{
				Set:  true,
				Kind: expand.String,
				Str:  strings.Repeat("x", 480),
			}, false)
			return &CommandResult{}, nil
		}).
		Command("observe", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			_, exists, _ = command.State().Variable("VALUE")
			return &CommandResult{}, nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: "mutate; observe"}); err != nil {
		t.Fatal(err)
	}
	if mutationErr == nil || !strings.Contains(mutationErr.Error(), "maximum materialized byte count 512 reached") {
		t.Fatalf("mutation error = %v", mutationErr)
	}
	if exists {
		t.Fatal("failed command-context mutation was committed")
	}
}

func TestUserHandlerCanChangeCurrentState(t *testing.T) {
	var observedDirectory string
	var observedValue string
	var observedResolved bool
	var observedEnvironment map[string]string

	simulator := NewBuilder().
		Command("change-state", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
			if err := command.State().ChangeDirectory(argumentString(t, invocation.Args[0])); err != nil {
				return nil, err
			}
			if err := command.State().SetVariable("CUSTOM", "value"); err != nil {
				return nil, err
			}
			return &CommandResult{}, nil
		}).
		Command("observe", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
			observedDirectory = invocation.Dir
			observedEnvironment = invocation.Env
			observedValue, _, observedResolved = command.State().Variable("CUSTOM")
			return &CommandResult{}, nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `change-state /workspace; observe`}); err != nil {
		t.Fatal(err)
	}
	if observedDirectory != "/workspace" {
		t.Fatalf("observed directory = %q, want /workspace", observedDirectory)
	}
	if observedValue != "value" || !observedResolved {
		t.Fatalf("observed variable = %q, resolved %v; want value, true", observedValue, observedResolved)
	}
	if observedEnvironment["PWD"] != "/workspace" || observedEnvironment["OLDPWD"] != "/" {
		t.Fatalf("observed environment = %#v", observedEnvironment)
	}
}

func TestUserHandlerCanSetAndUnsetVariables(t *testing.T) {
	var observed []string
	simulator := NewBuilder().
		Command("mutate", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
			switch argumentString(t, invocation.Args[0]) {
			case "set":
				if err := command.State().SetVariable("CUSTOM", "value"); err != nil {
					return nil, err
				}
			case "unset":
				if err := command.State().UnsetVariable("CUSTOM"); err != nil {
					return nil, err
				}
			}
			return &CommandResult{}, nil
		}).
		Command("observe", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			observed = append(observed, argumentString(t, invocation.Args[0]))
			return &CommandResult{}, nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `mutate set; observe "$CUSTOM"; mutate unset; observe "${CUSTOM-unset}"`}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"value", "unset"}; !reflect.DeepEqual(observed, want) {
		t.Fatalf("observed = %#v, want %#v", observed, want)
	}
}

func TestStateRejectsInvalidMutationsWithoutPartialCommit(t *testing.T) {
	largeValue := strings.Repeat("x", 2048)
	tests := []struct {
		name      string
		source    string
		limits    *Limits
		mutate    func(*State) error
		observe   func(*State) bool
		wantError string
	}{
		{
			name:      "invalid variable name",
			source:    `mutate; observe`,
			mutate:    func(state *State) error { return state.SetVariable("bad-name", "value") },
			observe:   func(state *State) bool { _, exists, _ := state.Variable("bad-name"); return !exists },
			wantError: "not a valid identifier",
		},
		{
			name:   "readonly variable",
			source: `readonly LOCKED=original; mutate; observe`,
			mutate: func(state *State) error { return state.SetVariable("LOCKED", "changed") },
			observe: func(state *State) bool {
				value, exists, resolved := state.Variable("LOCKED")
				return value == "original" && exists && resolved
			},
			wantError: "readonly variable",
		},
		{
			name:   "readonly directory variable",
			source: `readonly PWD; mutate; observe`,
			mutate: func(state *State) error { return state.ChangeDirectory("/changed") },
			observe: func(state *State) bool {
				value, exists, resolved := state.Variable("PWD")
				return state.Directory() == "/" && value == "/" && exists && resolved
			},
			wantError: "readonly variable",
		},
		{
			name:      "memory limit",
			source:    `mutate; observe`,
			limits:    &Limits{MaxExecutionSteps: 100, MaxMemoryBytes: 1024},
			mutate:    func(state *State) error { return state.SetVariable("LARGE", largeValue) },
			observe:   func(state *State) bool { _, exists, _ := state.Variable("LARGE"); return !exists },
			wantError: "maximum materialized byte count 1024 reached",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var mutationError error
			observed := false
			builder := NewBuilder()
			if test.limits != nil {
				builder.Limits(test.limits)
			}
			simulator := builder.
				Command("mutate", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
					mutationError = test.mutate(command.State())
					return &CommandResult{}, nil
				}).
				Command("observe", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
					observed = test.observe(command.State())
					return &CommandResult{}, nil
				}).
				Build()
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if mutationError == nil || !strings.Contains(mutationError.Error(), test.wantError) {
				t.Fatalf("mutation error = %v, want substring %q", mutationError, test.wantError)
			}
			if !observed {
				t.Fatal("failed mutation changed shell state")
			}
		})
	}
}

func TestUserHandlerStateIsIsolatedAcrossBranches(t *testing.T) {
	directories := make(map[string]int)
	simulator := NewBuilder().
		Command("mutate", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			if err := command.State().ChangeDirectory("/changed"); err != nil {
				return nil, err
			}
			return &CommandResult{}, nil
		}).
		Command("observe", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			directories[command.State().Directory()]++
			return &CommandResult{}, nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `if unknown-command; then mutate; fi; observe`}); err != nil {
		t.Fatal(err)
	}
	if directories["/"] != 1 || directories["/changed"] != 1 || len(directories) != 2 {
		t.Fatalf("observed directories = %#v", directories)
	}
}

func TestBuiltinCDUsesUserOverride(t *testing.T) {
	userCalls := 0
	observedDirectory := ""
	simulator := NewBuilder().
		Command("cd", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			userCalls++
			if err := command.State().ChangeDirectory("/custom"); err != nil {
				return nil, err
			}
			return &CommandResult{}, nil
		}).
		Command("observe", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			observedDirectory = command.State().Directory()
			return &CommandResult{}, nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `cd /ignored; builtin cd /; observe`}); err != nil {
		t.Fatal(err)
	}
	if userCalls != 2 || observedDirectory != "/custom" {
		t.Fatalf("user calls = %d, observed directory = %q", userCalls, observedDirectory)
	}
}

func TestBuiltinEchoUsesUserOverride(t *testing.T) {
	userCalls := 0
	var observed []string
	simulator := NewBuilder().
		Command("echo", func(_ context.Context, _ *CommandContext, _ *Invocation) (*CommandResult, error) {
			userCalls++
			return &CommandResult{Stdout: []byte("user\n")}, nil
		}).
		Command("observe", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			observed = argumentStrings(t, invocation)
			return &CommandResult{}, nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `direct=$(echo ignored); original=$(builtin echo original); observe "$direct" "$original"`}); err != nil {
		t.Fatal(err)
	}
	if userCalls != 2 || !reflect.DeepEqual(observed, []string{"user", "user"}) {
		t.Fatalf("user calls = %d, observed = %#v", userCalls, observed)
	}
}

func TestWildcardHandlerRunsOnlyAfterExactCommandMiss(t *testing.T) {
	var calls []string
	simulator := NewBuilder().
		Command("*", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			calls = append(calls, "wildcard:"+invocation.Name)
			return &CommandResult{}, nil
		}).
		Command("exact", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			calls = append(calls, "exact:"+invocation.Name)
			return &CommandResult{}, nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `exact; set -- value; missing`}); err != nil {
		t.Fatal(err)
	}
	want := []string{"exact:exact", "wildcard:missing"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestUserWildcardMakesUnknownCommandsObservableCandidates(t *testing.T) {
	var names []string
	consumerArgumentCount := 0
	consumerArgumentKind := ArgumentString
	simulator := NewBuilder().Command("*", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		names = append(names, invocation.Name)
		if invocation.Name == "producer" {
			return command.UnresolvedResult(), nil
		}
		consumerArgumentCount = len(invocation.Args)
		if consumerArgumentCount != 0 {
			consumerArgumentKind = invocation.Args[0].Kind
		}
		return &CommandResult{}, nil
	}).Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `for value in "$(producer)"; do consumer "$value"; done`}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"producer", "consumer"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("calls = %#v, want %#v", names, want)
	}
	if consumerArgumentCount != 1 || consumerArgumentKind != ArgumentUnresolved {
		t.Fatalf("consumer arguments = (%d, %d), want one unresolved argument", consumerArgumentCount, consumerArgumentKind)
	}
}

func TestUserHandlersOverrideCallLikeShellCommands(t *testing.T) {
	var calls []string
	builder := NewBuilder()
	for _, name := range []string{"builtin", "command", "exec", "eval", "source", ".", "test", "["} {
		commandName := name
		builder.Command(name, func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			calls = append(calls, commandName+":"+strings.Join(argumentStrings(t, invocation), ","))
			return &CommandResult{}, nil
		})
	}
	simulator := builder.Build()
	source := `builtin echo value
command echo value
exec echo value
eval echo value
source file
. file
test value
[ value`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"builtin:echo,value", "command:echo,value", "exec:echo,value", "eval:echo,value",
		"source:file", ".:file", "test:value", "[:value",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestLanguageControlCommandsRemainEvaluatorOwned(t *testing.T) {
	var controlCalls []string
	var records []string
	builder := NewBuilder()
	for _, name := range []string{"break", "continue", "return", "exit"} {
		commandName := name
		builder.Command(name, func(_ context.Context, _ *CommandContext, _ *Invocation) (*CommandResult, error) {
			controlCalls = append(controlCalls, commandName)
			return &CommandResult{}, nil
		})
	}
	builder.Command("record", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		records = append(records, argumentStrings(t, invocation)...)
		return &CommandResult{}, nil
	})
	simulator := builder.Build()
	source := `for value in one two; do
  record "continue:$value"
  continue
  record unreachable
done
for value in one two; do
  record "break:$value"
  builtin break
  record unreachable
done
f() {
  record before-return
  command return
  record unreachable
}
f
record before-exit
exit
record unreachable`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(controlCalls) != 0 {
		t.Fatalf("control handler calls = %#v, want none", controlCalls)
	}
	want := []string{"continue:one", "continue:two", "break:one", "before-return", "before-exit"}
	if !reflect.DeepEqual(records, want) {
		t.Fatalf("records = %#v, want %#v", records, want)
	}
}

func TestBuiltinTargetOverrideReceivesUnresolvedArguments(t *testing.T) {
	var arguments []*Argument
	simulator := NewBuilder().
		Command("echo", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			arguments = append(arguments, invocation.Args...)
			return &CommandResult{}, nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `builtin echo "$RANDOM"`}); err != nil {
		t.Fatal(err)
	}
	if len(arguments) != 1 || arguments[0].Kind != ArgumentUnresolved {
		t.Fatalf("arguments = %#v, want one unresolved argument", arguments)
	}
}

func TestSimulatorPropagatesHandlerError(t *testing.T) {
	sentinel := errors.New("handler failed")
	simulator := mustBuildSimulator(t, "lark-cli", func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
		return nil, sentinel
	})
	err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `lark-cli run`})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want handler error", err)
	}
}

func TestNilHandlerResultUsesUnresolvedFallback(t *testing.T) {
	var calls []string
	simulator := NewBuilder().
		Command("probe", func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
			return nil, nil
		}).
		Command("observe", recordFirstArgument(t, &calls)).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `if probe; then observe success; else observe failure; fi`}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"success", "failure"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorDispatchLimitsCombinedHandlerOutput(t *testing.T) {
	const maximum = 2048
	for _, test := range []struct {
		name    string
		stdout  string
		stderr  string
		wantErr bool
	}{
		{name: "within limit", stdout: strings.Repeat("x", maximum/4), stderr: strings.Repeat("y", maximum/4)},
		{name: "one byte over", stdout: strings.Repeat("x", maximum/2), stderr: strings.Repeat("y", maximum/2+1), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			simulator := NewBuilder().
				Limits(&Limits{MaxExecutionSteps: 100, MaxMemoryBytes: maximum}).
				Command("output", func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
					return &CommandResult{Stdout: []byte(test.stdout), Stderr: []byte(test.stderr)}, nil
				}).
				Build()
			err := simulator.Simulate(context.Background(), &SimulationRequest{Source: "output"})
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 2048 reached") {
					t.Fatalf("Simulate() error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSimulatorAcceptsUnresolvedHandlerResult(t *testing.T) {
	simulator := mustBuildSimulator(t, "unknown", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
		return command.UnresolvedResult(), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `unknown`}); err != nil {
		t.Fatal(err)
	}
}

func TestSimulatorConvertsHandlerPanicToError(t *testing.T) {
	simulator := mustBuildSimulator(t, "lark-cli", func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
		panic("handler panic")
	})
	err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `lark-cli`})
	if err == nil || !strings.Contains(err.Error(), `execute command "lark-cli" panicked: handler panic`) {
		t.Fatalf("Simulate() error = %v", err)
	}
}

func TestSimulatorHonorsCancellationDuringParsing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	simulator := mustBuildSimulator(t, "lark-cli", func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
		t.Fatal("handler called after cancellation")
		return nil, nil
	})
	err := simulator.Simulate(ctx, &SimulationRequest{Source: `if`})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Simulate() error = %v, want context.Canceled", err)
	}
}

func TestSimulatorHonorsCancellationAfterHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	simulator := mustBuildSimulator(t, "lark-cli", func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
		cancel()
		return &CommandResult{}, nil
	})
	err := simulator.Simulate(ctx, &SimulationRequest{Source: `lark-cli`})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Simulate() error = %v, want context.Canceled", err)
	}
}

func TestSimulatorIsolatesHandlerInvocation(t *testing.T) {
	request := &SimulationRequest{
		Source: `printf ':' > helper.sh; echo changed > virtual.txt; source helper.sh; lark-cli "$TOKEN" "$1"`,
		Env:    map[string]string{"TOKEN": "original"},
		Args:   []string{"argument"},
		Stdin:  []byte("input"),
	}
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		invocation.Args[0].Value = "mutated"
		invocation.Env["TOKEN"] = "mutated"
		invocation.Stdin[0] = 'X'
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if request.Env["TOKEN"] != "original" || !reflect.DeepEqual(request.Args, []string{"argument"}) || string(request.Stdin) != "input" {
		t.Fatalf("request mutated: %#v %q", request.Env, request.Stdin)
	}
}

func TestSimulatorDoesNotMutateHandlerResult(t *testing.T) {
	result := &CommandResult{Stdout: []byte("output"), Stderr: []byte("error")}
	simulator := mustBuildSimulator(t, "lark-cli", func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
		return result, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `lark-cli output`}); err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) != "output" || string(result.Stderr) != "error" {
		t.Fatal("Simulate mutated the handler-owned result")
	}
}

func TestSimulatorPassesOwnedInvocationToHandler(t *testing.T) {
	var handledInvocation *Invocation
	simulator := mustBuildSimulator(t, "handled", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		handledInvocation = invocation
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `handled arg`}); err != nil {
		t.Fatal(err)
	}
	if handledInvocation == nil || handledInvocation.Name != "handled" || !reflect.DeepEqual(handledInvocation.Args, []*Argument{{Kind: ArgumentString, Value: "arg"}}) {
		t.Fatalf("handler invocation = %#v", handledInvocation)
	}
}

func TestSimulatorIgnoresHostDependencyInInactiveParameterOperands(t *testing.T) {
	var calls [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, argumentStrings(t, invocation))
		return &CommandResult{}, nil
	})

	source := `value=known
lark-cli "${value:-$RANDOM}" "${missing:+$RANDOM}"
[[ "${value:-$RANDOM}" == known ]] || lark-cli test-wrong
case "${value:-$RANDOM}" in known) :;; *) lark-cli case-wrong;; esac
value=1; (( ${value:-$RANDOM} )) || lark-cli arithmetic-wrong`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"known", ""}}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorPreservesBackgroundOutputInCommandSubstitution(t *testing.T) {
	var consumed string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		switch argumentString(t, invocation.Args[0]) {
		case "produce":
			return &CommandResult{Stdout: []byte("background\n")}, nil
		case "consume":
			consumed = argumentString(t, invocation.Args[1])
		}
		return &CommandResult{}, nil
	})

	source := `lark-cli consume "$(lark-cli produce & wait)"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if consumed != "background" {
		t.Fatalf("consumed = %q, want background", consumed)
	}
}

func TestSimulatorPreservesCommandSubstitutionStderr(t *testing.T) {
	var consumed []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		switch argumentString(t, invocation.Args[0]) {
		case "foreground":
			return &CommandResult{Stderr: []byte("foreground-error\n")}, nil
		case "background":
			return &CommandResult{Stderr: []byte("background-error\n")}, nil
		case "consume":
			consumed = argumentStrings(t, &Invocation{Args: invocation.Args[1:]})
		}
		return &CommandResult{}, nil
	})

	source := `foreground=$(lark-cli foreground) 2> foreground.err
background=$(lark-cli background & wait) 2> background.err
lark-cli consume "$(<foreground.err)" "$(<background.err)"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	want := []string{"foreground-error", "background-error"}
	if !reflect.DeepEqual(consumed, want) {
		t.Fatalf("consumed = %#v, want %#v", consumed, want)
	}
}

func TestSimulatorExecutesCommandWrappedSpecialBuiltins(t *testing.T) {
	source := `eval() { lark-cli function-eval; }
command -- eval 'lark-cli evaluated'
command -p test 1 = 1 && lark-cli test-true
command test 1 = 2 || lark-cli test-false
echo 'lark-cli sourced' > virtual.sh
command . virtual.sh
command() { lark-cli function-command; }
command eval 'lark-cli should-not-evaluate'`
	requireFirstArguments(t, source, []string{"evaluated", "test-true", "test-false", "sourced", "function-command"})
}

func TestSimulatorExecutesDynamicCommandWrappedSpecialBuiltins(t *testing.T) {
	source := `name=eval; command "$name" 'lark-cli evaluated'
name=test; command "$name" 1 = 2 || lark-cli test-false
echo 'lark-cli sourced' > virtual.sh
name=source; command "$name" virtual.sh`
	requireFirstArguments(t, source, []string{"evaluated", "test-false", "sourced"})
}

func TestSimulatorDoesNotTreatFunctionNamedTestAsBuiltin(t *testing.T) {
	callbacks := 0
	simulator := mustBuildSimulator(t, "lark-cli", countInvocations(&callbacks))

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `test() { lark-cli "$1"; }; test "$RANDOM"`,
	})
	if err == nil || !strings.Contains(err.Error(), "host runtime state") {
		t.Fatalf("Simulate() error = %v", err)
	}
	if callbacks != 0 {
		t.Fatalf("callbacks = %d, want 0", callbacks)
	}
}

func requireFirstArguments(t testing.TB, source string, want []string) {
	t.Helper()
	requireArguments(t, source, want, recordFirstArgument)
}

func requireJoinedArguments(t testing.TB, source string, want []string) {
	t.Helper()
	requireArguments(t, source, want, recordJoinedArguments)
}

func requireArguments(t testing.TB, source string, want []string, recorder func(testing.TB, *[]string) Command) {
	t.Helper()
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recorder(t, &calls))
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func recordFirstArgument(t testing.TB, calls *[]string) Command {
	return func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		*calls = append(*calls, argumentString(t, invocation.Args[0]))
		return &CommandResult{}, nil
	}
}

func recordJoinedArguments(t testing.TB, calls *[]string) Command {
	return func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		*calls = append(*calls, strings.Join(argumentStrings(t, invocation), " "))
		return &CommandResult{}, nil
	}
}

func countInvocations(callbacks *int) Command {
	return func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
		*callbacks++
		return &CommandResult{}, nil
	}
}

func successfulHandler() Command {
	return func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
		return &CommandResult{}, nil
	}
}

func argumentStrings(t testing.TB, invocation *Invocation) []string {
	t.Helper()
	args := invocation.Args
	if args == nil {
		return nil
	}
	values := make([]string, len(args))
	for index, argument := range args {
		if argument.Kind != ArgumentString {
			t.Fatalf("argument %d is unresolved: %#v", index, argument)
		}
		values[index] = argument.Value
	}
	return values
}

func argumentString(t testing.TB, argument *Argument) string {
	t.Helper()
	if argument.Kind != ArgumentString {
		t.Fatalf("argument is unresolved: %#v", argument)
	}
	return argument.Value
}

func mustBuildSimulator(t testing.TB, name string, handler Command) *Simulator {
	t.Helper()
	return NewBuilder().Command(name, handler).Build()
}
