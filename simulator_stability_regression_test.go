package libcommand

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestSimulatorRestoresTemporaryAssignmentsOnUnknownFailure(t *testing.T) {
	source := `value=outer
if value=temporary cd /missing; then
  :
else
  lark-cli "$value"
fi`
	requireFirstArguments(t, source, []string{"outer"})
}

func TestSimulatorCorrelatesInputOnlySubstitutionFailureState(t *testing.T) {
	source := `value=outer
if value=$(< /missing/file); then
  :
else
  if [[ -e /missing/file ]]; then lark-cli leaked; else lark-cli "clean:$value"; fi
fi`
	requireFirstArguments(t, source, []string{"clean:"})
}

func TestSimulatorExploresEachUnknownRedirectionFailure(t *testing.T) {
	source := `if : > /first/file > /second/file; then
  lark-cli success
else
  if [[ -e /first/file ]]; then lark-cli failure-second; else lark-cli failure-first; fi
fi`
	requireFirstArguments(t, source, []string{"success", "failure-first", "failure-second"})
}

func TestSimulatorRetainsCorrelatedStateAfterExitStatusIsOverwritten(t *testing.T) {
	source := `{
  : > /missing/file
  :
  if [[ -e /missing/file ]]; then lark-cli success; else lark-cli failure; fi
}`
	requireFirstArguments(t, source, []string{"success", "failure"})
}

func TestSimulatorRetainsEarlierCommandSubstitutionAlternatives(t *testing.T) {
	var calls [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		arguments := make([]string, 0, len(invocation.Args))
		for _, argument := range invocation.Args {
			if argument.Kind == ArgumentUnresolved {
				arguments = append(arguments, "<unresolved>")
				continue
			}
			arguments = append(arguments, argument.Value)
		}
		calls = append(calls, arguments)
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `lark-cli "$(< /missing/file)" "$(true)"`,
	}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"<unresolved>", ""}, {"", ""}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorCorrelatesNegatedUnknownState(t *testing.T) {
	source := `{
  if ! cd /missing; then
	    lark-cli then "$PWD"
  else
	    lark-cli else "$PWD"
  fi
}`
	requireFirstArguments(t, source, []string{"else", "then"})
}

func TestSimulatorRetainsUnknownPipelineState(t *testing.T) {
	source := `{
  : > /missing/file | :
  if [[ -e /missing/file ]]; then lark-cli success; else lark-cli failure; fi
}`
	requireFirstArguments(t, source, []string{"success", "failure"})
}

func TestSimulatorRetainsUnknownBackgroundState(t *testing.T) {
	source := `{
  : > /missing/file &
  wait
  if [[ -e /missing/file ]]; then lark-cli success; else lark-cli failure; fi
}`
	requireFirstArguments(t, source, []string{"success", "failure"})
}

func TestSimulatorDoesNotDuplicateRedirectionFailureAcrossInnerBranches(t *testing.T) {
	source := `{
  { if unknown-command; then branch=a; else branch=b; fi; } > /missing/file
  if [[ -e /missing/file ]]; then
    lark-cli "success:$branch"
  else
    lark-cli "failure:${branch-unset}"
  fi
}`
	requireFirstArguments(t, source, []string{"success:a", "success:b", "failure:unset"})
}

func TestSimulatorPreservesSparseIndexedArrayMutations(t *testing.T) {
	var calls [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, argumentStrings(t, invocation))
		return &CommandResult{}, nil
	})
	source := `arithmetic=([2]=two)
((arithmetic[4]=4))
lark-cli arithmetic "${!arithmetic[@]}" "${arithmetic[2]-missing}" "${arithmetic[4]-missing}" "${arithmetic[0]-missing}"
parameter=([2]=two)
: "${parameter[4]:=four}"
lark-cli parameter "${!parameter[@]}" "${parameter[@]}" "${parameter[0]-missing}"`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"arithmetic", "2", "4", "two", "4", "missing"},
		{"parameter", "2", "4", "two", "four", "missing"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorSlicesScalarValuesByUnicodeCharacters(t *testing.T) {
	source := `value=你好世界
array=([2]=你好)
dense=(zero one two)
lark-cli "${value:1:1}" "${value: -2}" "${value:1:-1}" "${array[2]:1:1}" "${dense[@]:1:2}"`
	var calls [][]string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, argumentStrings(t, invocation))
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"好", "世界", "好世", "好", "one", "two"}}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorPreservesTopLevelStdinConsumptionAcrossOutputRedirection(t *testing.T) {
	source := `read first > /dev/null
read second
lark-cli "$first:$second"`
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	request := &SimulationRequest{Source: source, Stdin: []byte("one\ntwo\n")}
	if err := simulator.Simulate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if want := []string{"one:two"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorPreservesInheritedStdinConsumptionAcrossPipeline(t *testing.T) {
	source := `{ read first; printf ignored; } | :
read second
lark-cli "$second"`
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	request := &SimulationRequest{Source: source, Stdin: []byte("one\ntwo\n")}
	if err := simulator.Simulate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if want := []string{"two"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorUsesDevNullForBackgroundStdin(t *testing.T) {
	source := `read background & wait
read foreground
lark-cli "$foreground"`
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
	request := &SimulationRequest{Source: source, Stdin: []byte("one\ntwo\n")}
	if err := simulator.Simulate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if want := []string{"one"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorLimitsInitialASTMaterialization(t *testing.T) {
	const maximum = 256
	simulator := NewBuilder().Limits(&Limits{
		MaxExecutionSteps: 100,
		MaxMemoryBytes:    maximum,
	}).Build()
	source := strings.Repeat(":\n", 20)
	if len(source) >= maximum {
		t.Fatalf("test source size = %d, want below %d", len(source), maximum)
	}
	err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source})
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 256 reached") {
		t.Fatalf("Simulate() error = %v", err)
	}
}

func TestSimulatorReturnsErrorForInvalidExpandedGlob(t *testing.T) {
	simulator := NewBuilder().Build()

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: "$1",
		Args:   []string{`[\0]`},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid pathname expansion pattern") {
		t.Fatalf("Simulate() error = %v", err)
	}
}
