package builtin

import (
	"context"
	"reflect"
	"testing"

	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/expand"
)

func executeBuiltinScript(t *testing.T, source string, request *runtime.Request, extra map[string]*runtime.CommandDefinition) error {
	t.Helper()
	definitions := Definitions()
	for name, definition := range extra {
		definitions[name] = definition
	}
	file, err := runtime.Parse(context.Background(), source, "builtin-test.sh")
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(name string) *runtime.CommandDefinition {
		if definition := definitions[name]; definition != nil {
			return definition
		}
		return definitions["*"]
	}
	execution := runtime.NewExecutionContext(context.Background(), &runtime.Config{
		LookupCommand:     lookup,
		MaxExecutionSteps: 200,
		MaxMemoryBytes:    2 << 20,
	})
	return execution.Execute(file, request)
}

func recordingCommand(calls *[][]string, inspect func(*runtime.CommandContext, *runtime.Invocation)) *runtime.CommandDefinition {
	return &runtime.CommandDefinition{
		Candidate: true,
		Command: func(_ context.Context, execution *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
			arguments := make([]string, len(invocation.Args))
			for index, argument := range invocation.Args {
				arguments[index] = argument.Value
			}
			*calls = append(*calls, arguments)
			if inspect != nil {
				inspect(execution, invocation)
			}
			return &runtime.CommandResult{}, nil
		},
	}
}

func TestEnvUsesUserCommandAndFallbackDispatch(t *testing.T) {
	var calls []string
	record := func(label string) *runtime.CommandDefinition {
		return &runtime.CommandDefinition{
			Command: func(_ context.Context, execution *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
				calls = append(calls, label+":"+invocation.Name)
				return &runtime.CommandResult{}, nil
			},
			UserOverride: true,
		}
	}
	echo := record("exact")
	echo.Builtin = true
	fallback := record("fallback")
	fallback.Fallback = true
	err := executeBuiltinScript(t, `env echo exact; env missing fallback`, &runtime.Request{}, map[string]*runtime.CommandDefinition{
		"echo": echo,
		"*":    fallback,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"exact:echo", "fallback:missing"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestEchoKeepsKnownSuccessWithUnresolvedArgument(t *testing.T) {
	var calls [][]string
	err := executeBuiltinScript(t, `if echo "$(missing)"; then record success; else record failure; fi`, &runtime.Request{}, map[string]*runtime.CommandDefinition{
		"record": recordingCommand(&calls, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"success"}}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestExecUnresolvedCommandNameFailsWithoutStoppingEvaluation(t *testing.T) {
	var calls [][]string
	err := executeBuiltinScript(t, `if exec "$(missing)"; then record success; else record failure; fi; record after`, &runtime.Request{}, map[string]*runtime.CommandDefinition{
		"record": recordingCommand(&calls, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"failure"}, {"after"}}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestReadInvalidOptionIsAtomic(t *testing.T) {
	var calls [][]string
	err := executeBuiltinScript(t,
		`value=original; read -Z value; read next; record "$value:$next"`,
		&runtime.Request{Stdin: []byte("representative\n")},
		map[string]*runtime.CommandDefinition{"record": recordingCommand(&calls, nil)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"original:representative"}}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestLoopControlRejectsZeroDepth(t *testing.T) {
	var calls [][]string
	err := executeBuiltinScript(t,
		`for value in one; do break 0 || record break; continue 0 || record continue; done`,
		&runtime.Request{},
		map[string]*runtime.CommandDefinition{"record": recordingCommand(&calls, nil)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"break"}, {"continue"}}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestStatefulBuiltinsAndMalformedArguments(t *testing.T) {
	var calls [][]string
	source := `
printf -v formatted '%s:%s' one two
read first rest
f() { :; }
unset -f f
set -- one two
shift 1
record "$formatted" "$first" "$rest" "$#" "$1"
unset -q 2>/dev/null || record unset-error
shift x 2>/dev/null || record shift-error
wait 1 2>/dev/null || record wait-error
printf 2>/dev/null || record printf-error
cd a b 2>/dev/null || record cd-error
wait && record wait-success
return 2>/dev/null || record return-error
`
	err := executeBuiltinScript(t, source, &runtime.Request{Stdin: []byte("one two three\n")}, map[string]*runtime.CommandDefinition{
		"record": recordingCommand(&calls, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"one:two", "one", "two three", "1", "two"},
		{"unset-error"}, {"shift-error"}, {"wait-error"}, {"printf-error"},
		{"cd-error"}, {"wait-success"}, {"return-error"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestCDSearchesKnownCDPATH(t *testing.T) {
	var calls [][]string
	prepare := &runtime.CommandDefinition{
		Candidate: true,
		Command: func(_ context.Context, execution *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
			if err := execution.EnsureDirectory("/work"); err != nil {
				return nil, err
			}
			if err := execution.EnsureDirectory("/candidate/target"); err != nil {
				return nil, err
			}
			if err := execution.State().ChangeDirectory("/work"); err != nil {
				return nil, err
			}
			if err := execution.AssignVariable("CDPATH", &expand.Variable{Set: true, Kind: expand.String, Str: "/candidate"}, false); err != nil {
				return nil, err
			}
			return &runtime.CommandResult{}, nil
		},
	}
	err := executeBuiltinScript(t, `prepare; cd target; record "$PWD"`, &runtime.Request{}, map[string]*runtime.CommandDefinition{
		"prepare": prepare,
		"record": recordingCommand(&calls, func(execution *runtime.CommandContext, _ *runtime.Invocation) {
			directory, unknown := execution.Directory()
			if unknown || directory != "/candidate/target" {
				t.Fatalf("directory = %q, unknown = %v", directory, unknown)
			}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"/candidate/target"}}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestCDPreservesUnknownCDPATHDependency(t *testing.T) {
	var calls [][]string
	prepare := &runtime.CommandDefinition{
		Candidate: true,
		Command: func(_ context.Context, execution *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
			if err := execution.EnsureDirectory("/work/target"); err != nil {
				return nil, err
			}
			if err := execution.State().ChangeDirectory("/work"); err != nil {
				return nil, err
			}
			if err := execution.AssignVariable("CDPATH", &expand.Variable{Set: true, Kind: expand.String, Str: "/candidate"}, true); err != nil {
				return nil, err
			}
			return &runtime.CommandResult{}, nil
		},
	}
	err := executeBuiltinScript(t, `prepare; cd target; record`, &runtime.Request{}, map[string]*runtime.CommandDefinition{
		"prepare": prepare,
		"record": recordingCommand(&calls, func(execution *runtime.CommandContext, invocation *runtime.Invocation) {
			directory, unknown := execution.Directory()
			if directory != "/work/target" || !unknown || invocation.Unresolved == nil || !invocation.Unresolved.Dir {
				t.Fatalf("directory = %q, unknown = %v, invocation = %#v", directory, unknown, invocation)
			}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v", calls)
	}
}
