package builtin

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/expand"
)

func TestReadCancellationAndOptionEdges(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := executeRead(cancelled, nil, &runtime.Invocation{}); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read = %#v, %v", result, err)
	}

	var calls [][]string
	source := `
read -s -e <<< quiet
record "$REPLY"
read -N 3 exact <<< abcdef
record "$exact"
read -d '' nul <<< value
record "$nul"
read -n 2>/dev/null || record missing-n
read -N nope 2>/dev/null || record invalid-N
read -u 2>/dev/null || record missing-u
read -u nope 2>/dev/null || record invalid-u
read -p 2>/dev/null || record missing-p
read -i 2>/dev/null || record missing-i
`
	if err := executeBuiltinScript(t, source, &runtime.Request{}, map[string]*runtime.CommandDefinition{"record": recordingCommand(&calls, nil)}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"quiet"}, {"abc"}, {"value"}, {"missing-n"}, {"invalid-N"}, {"missing-u"}, {"invalid-u"}, {"missing-p"}, {"missing-i"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestReadAndMapfilePropagateUnresolvedInput(t *testing.T) {
	checkedRead := false
	checkedMapfile := false
	definitions := map[string]*runtime.CommandDefinition{
		"read-probe": {
			Command: func(ctx context.Context, shell *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
				shell.SetInput([]byte("representative"), true)
				result, err := executeRead(ctx, shell, &runtime.Invocation{Name: "read", Args: []*runtime.Argument{{Kind: runtime.ArgumentString, Value: "value"}}})
				if err != nil || !shell.VariableUnknown("value") {
					t.Fatalf("unresolved read = %#v, %v, unknown=%v", result, err, shell.VariableUnknown("value"))
				}
				input, unresolved := shell.Input()
				if len(input) != 0 || unresolved {
					t.Fatalf("read input after unresolved consumption = %q, %v", input, unresolved)
				}
				checkedRead = true
				return result, nil
			},
		},
		"mapfile-probe": {
			Command: func(ctx context.Context, shell *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
				shell.SetInput([]byte("representative"), true)
				result, err := executeMapfile(ctx, shell, &runtime.Invocation{Name: "mapfile", Args: []*runtime.Argument{{Kind: runtime.ArgumentString, Value: "values"}}})
				if err != nil || !shell.VariableUnknown("values") || shell.Variable("values").Kind != expand.Indexed {
					t.Fatalf("unresolved mapfile = %#v, %v, value=%#v unknown=%v", result, err, shell.Variable("values"), shell.VariableUnknown("values"))
				}
				checkedMapfile = true
				return result, nil
			},
		},
	}
	if err := executeBuiltinScript(t, "read-probe; mapfile-probe", &runtime.Request{}, definitions); err != nil {
		t.Fatal(err)
	}
	if !checkedRead || !checkedMapfile {
		t.Fatalf("probes = read %v, mapfile %v", checkedRead, checkedMapfile)
	}
}

func TestInputBuiltinsRejectUnresolvedArguments(t *testing.T) {
	checked := 0
	probe := &runtime.CommandDefinition{
		Command: func(ctx context.Context, shell *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
			invocation := &runtime.Invocation{Args: []*runtime.Argument{{Kind: runtime.ArgumentUnresolved}}}
			for _, command := range []runtime.Command{executeRead, executeMapfile} {
				result, err := command(ctx, shell, invocation)
				if err != nil || result == nil || len(result.Outputs()) != 1 {
					t.Fatalf("unresolved input builtin = %#v, %v", result, err)
				}
				_, stderrUnresolved := result.Outputs()[0].Stderr.Data()
				if !stderrUnresolved {
					t.Fatal("unresolved input builtin stderr was marked resolved")
				}
				checked++
			}
			return shell.Result(shell.Output().Build()), nil
		},
	}
	if err := executeBuiltinScript(t, "probe", &runtime.Request{}, map[string]*runtime.CommandDefinition{"probe": probe}); err != nil {
		t.Fatal(err)
	}
	if checked != 2 {
		t.Fatalf("checked = %d, want 2", checked)
	}
}
