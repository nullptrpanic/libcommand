package libcommand

import (
	"bytes"
	"context"
	"maps"
	"reflect"
	"strings"
	"testing"
)

type fuzzInvocation struct {
	Name       string
	Args       []*fuzzArgument
	Env        map[string]string
	Dir        string
	Stdin      []byte
	Unresolved *InvocationUnresolved
}

type fuzzArgument struct {
	Kind  ArgumentKind
	Value string
}

func FuzzSimulatorSourceStability(f *testing.F) {
	seeds := []string{
		`record literal`,
		`value=one; if test -n "$value"; then record "$value"; fi`,
		`for value in one two; do record "$value"; done`,
		`record "$(printf generated)" | record consume`,
		`record background & wait`,
		`declare -A values=([b]=two [a]=one); record "${!values[@]}" "${values[@]}"`,
		`declare -A values=(value-without-key)`,
		`if true; then`,
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	simulator := mustBuildSimulator(f, "record", successfulHandler())
	f.Fuzz(func(_ *testing.T, source string) {
		_ = simulator.Simulate(context.Background(), &SimulationRequest{Source: source})
	})
}

func FuzzSimulatorDeterministicReplay(f *testing.F) {
	f.Add(
		`record "$TOKEN" "$1"`,
		"environment",
		"argument",
		[]byte("stdin\n"),
	)
	f.Fuzz(func(t *testing.T, source, environment, argument string, stdin []byte) {
		request := &SimulationRequest{
			Source: source,
			Env:    map[string]string{"TOKEN": environment},
			Args:   []string{argument},
			Stdin:  stdin,
		}
		firstCalls, firstErr := runFuzzSimulation(t, request)
		secondCalls, secondErr := runFuzzSimulation(t, request)
		firstError := fuzzErrorText(firstErr)
		secondError := fuzzErrorText(secondErr)
		if firstError != secondError {
			t.Fatalf("errors differ: first=%q second=%q", firstError, secondError)
		}
		if !reflect.DeepEqual(firstCalls, secondCalls) {
			t.Fatalf("invocations differ: first=%#v second=%#v", firstCalls, secondCalls)
		}
	})
}

func FuzzSimulatorRequestIsolation(f *testing.F) {
	f.Add("token", "argument", []byte("request input\n"))
	f.Fuzz(func(t *testing.T, token, argument string, stdin []byte) {
		var calls []*fuzzInvocation
		simulator := mustBuildSimulator(t, "record", Command(
			func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
				calls = append(calls, snapshotFuzzInvocation(t, invocation))
				return &CommandResult{}, nil
			},
		))
		const source = `record request "$TOKEN" "$1"`

		first := &SimulationRequest{
			Source: source,
			Env:    map[string]string{"TOKEN": token},
			Args:   []string{argument},
			Stdin:  stdin,
		}
		if err := simulator.Simulate(context.Background(), first); err != nil {
			t.Fatalf("first simulation: %v", err)
		}

		secondToken := "second:" + token
		secondArgument := "second:" + argument
		secondStdin := append(append([]byte(nil), stdin...), []byte("second")...)
		second := &SimulationRequest{
			Source: source,
			Env:    map[string]string{"TOKEN": secondToken},
			Args:   []string{secondArgument},
			Stdin:  secondStdin,
		}
		if err := simulator.Simulate(context.Background(), second); err != nil {
			t.Fatalf("second simulation: %v", err)
		}

		if len(calls) != 2 {
			t.Fatalf("calls=%#v, want two invocations", calls)
		}
		assertFuzzInvocation(t, calls[0], []string{"request", token, argument}, token, stdin)
		assertFuzzInvocation(t, calls[1], []string{"request", secondToken, secondArgument}, secondToken, secondStdin)
	})
}

func runFuzzSimulation(t testing.TB, request *SimulationRequest) ([]*fuzzInvocation, error) {
	t.Helper()
	var calls []*fuzzInvocation
	simulator := mustBuildSimulator(t, "record", Command(
		func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			calls = append(calls, snapshotFuzzInvocation(t, invocation))
			return &CommandResult{}, nil
		},
	))
	err := simulator.Simulate(context.Background(), request)
	return calls, err
}

func snapshotFuzzInvocation(t testing.TB, invocation *Invocation) *fuzzInvocation {
	t.Helper()
	return &fuzzInvocation{
		Name:       invocation.Name,
		Args:       fuzzArguments(invocation),
		Env:        maps.Clone(invocation.Env),
		Dir:        invocation.Dir,
		Stdin:      append([]byte(nil), invocation.Stdin...),
		Unresolved: cloneInvocationUnresolved(invocation.Unresolved),
	}
}

func fuzzArguments(invocation *Invocation) []*fuzzArgument {
	arguments := make([]*fuzzArgument, len(invocation.Args))
	for index, argument := range invocation.Args {
		arguments[index] = &fuzzArgument{Kind: argument.Kind, Value: argument.Value}
	}
	return arguments
}

func cloneInvocationUnresolved(unresolved *InvocationUnresolved) *InvocationUnresolved {
	if unresolved == nil {
		return nil
	}
	cloned := *unresolved
	cloned.Env = append([]string(nil), unresolved.Env...)
	return &cloned
}

func assertFuzzInvocation(t testing.TB, got *fuzzInvocation, args []string, token string, stdin []byte) {
	t.Helper()
	wantArgs := make([]*fuzzArgument, len(args))
	for index, argument := range args {
		wantArgs[index] = &fuzzArgument{Kind: ArgumentString, Value: shellString(argument)}
	}
	if got.Name != "record" || !reflect.DeepEqual(got.Args, wantArgs) || got.Env["TOKEN"] != shellString(token) || got.Dir != "/" || !bytes.Equal(got.Stdin, stdin) || got.Unresolved != nil {
		t.Fatalf("invocation=%#v, want args=%#v token=%q stdin=%q", got, args, token, stdin)
	}
}

func shellString(value string) string {
	if index := strings.IndexByte(value, 0); index >= 0 {
		return value[:index]
	}
	return value
}

func fuzzErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
