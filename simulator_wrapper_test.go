package libcommand

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestSimulationUserAndCommandScopedChangeUser(t *testing.T) {
	var observed []string
	simulator := NewBuilder().
		Command("run-as", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
			if err := command.ChangeUser(argumentString(t, invocation.Args[0])); err != nil {
				return nil, err
			}
			return command.Invoke("record", invocation.Args[1:], false), nil
		}).
		Command("record", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
			observed = append(observed, command.State().User()+":"+argumentString(t, invocation.Args[0]))
			return &CommandResult{}, nil
		}).
		Build()

	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `record before; run-as root nested; record after`,
		User:   "alice",
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"alice:before", "root:nested", "alice:after"}
	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("observed users = %#v, want %#v", observed, want)
	}

	observed = nil
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `record default`}); err != nil {
		t.Fatal(err)
	}
	if want = []string{"user:default"}; !reflect.DeepEqual(observed, want) {
		t.Fatalf("default user = %#v, want %#v", observed, want)
	}
}

func TestExecutionWrappersDispatchThroughMiddlewareAndRestoreUser(t *testing.T) {
	var middlewareCalls []string
	var records []string
	middleware := func(next Command) Command {
		return func(ctx context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
			middlewareCalls = append(middlewareCalls, invocation.Name+":"+command.State().User())
			return next(ctx, command, invocation)
		}
	}
	simulator := NewBuilder().
		Middleware(middleware).
		Command("record", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
			records = append(records, command.State().User()+":"+argumentString(t, invocation.Args[0]))
			return &CommandResult{}, nil
		}).
		Build()

	source := `sudo -u root -- setsid -- nohup timeout 5 nice -n 3 stdbuf -oL taskset -c 0 ionice -c 2 chrt -r 1 record nested
record after`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{
		"sudo:user",
		"setsid:root",
		"nohup:root",
		"timeout:root",
		"nice:root",
		"stdbuf:root",
		"taskset:root",
		"ionice:root",
		"chrt:root",
		"record:root",
		"record:user",
	}
	if !reflect.DeepEqual(middlewareCalls, wantCalls) {
		t.Fatalf("middleware calls = %#v, want %#v", middlewareCalls, wantCalls)
	}
	if wantRecords := []string{"root:nested", "user:after"}; !reflect.DeepEqual(records, wantRecords) {
		t.Fatalf("records = %#v, want %#v", records, wantRecords)
	}
}

func TestSudoUsesRequestedUserAndTemporaryEnvironment(t *testing.T) {
	var observed []string
	simulator := NewBuilder().
		Command("record", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
			observed = append(observed, command.State().User()+":"+invocation.Env["TOKEN"])
			return &CommandResult{}, nil
		}).
		Build()

	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `sudo TOKEN=inner record; record`,
		Env:    map[string]string{"TOKEN": "outer"},
		User:   "alice",
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"root:inner", "alice:outer"}
	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("observed = %#v, want %#v", observed, want)
	}
}

func TestExecutionWrapperCommonOptionForms(t *testing.T) {
	tests := []*struct {
		name       string
		source     string
		wantedUser string
	}{
		{name: "sudo combined options", source: `sudo -nE -uroot record value`, wantedUser: "root"},
		{name: "setsid combined options", source: `setsid -fcw record value`, wantedUser: "user"},
		{name: "nohup option terminator", source: `nohup -- record value`, wantedUser: "user"},
		{name: "timeout attached options", source: `timeout -sTERM -k1 5 record value`, wantedUser: "user"},
		{name: "nice legacy adjustment", source: `nice -10 record value`, wantedUser: "user"},
		{name: "stdbuf attached modes", source: `stdbuf -i0 -oL record value`, wantedUser: "user"},
		{name: "taskset long cpu list", source: `taskset --cpu-list 0 record value`, wantedUser: "user"},
		{name: "ionice attached values", source: `ionice -c2 -n7 record value`, wantedUser: "user"},
		{name: "chrt round robin", source: `chrt --rr 1 record value`, wantedUser: "user"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			simulator := NewBuilder().Command("record", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
				called = true
				if got := command.State().User(); got != test.wantedUser {
					t.Fatalf("record user = %q, want %q", got, test.wantedUser)
				}
				if got := argumentString(t, invocation.Args[0]); got != "value" {
					t.Fatalf("record argument = %q, want value", got)
				}
				return &CommandResult{}, nil
			}).Build()
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("nested record command was not dispatched")
			}
		})
	}
}

func TestExecutionWrappersFailOpenWhenNoNestedCommandCanBeSelected(t *testing.T) {
	tests := []*struct {
		name   string
		source string
	}{
		{name: "sudo query", source: `sudo -l; record after`},
		{name: "setsid unsupported option", source: `setsid --unsupported record nested; record after`},
		{name: "nohup unsupported option", source: `nohup --unsupported record nested; record after`},
		{name: "timeout unsupported option", source: `timeout --unsupported record nested; record after`},
		{name: "nice unsupported option", source: `nice --unsupported record nested; record after`},
		{name: "stdbuf unsupported option", source: `stdbuf --unsupported record nested; record after`},
		{name: "taskset unsupported option", source: `taskset --unsupported record nested; record after`},
		{name: "ionice unsupported option", source: `ionice --unsupported record nested; record after`},
		{name: "chrt unsupported option", source: `chrt --unsupported record nested; record after`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			simulator := NewBuilder().Command("record", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
				calls = append(calls, argumentString(t, invocation.Args[0]))
				return &CommandResult{}, nil
			}).Build()

			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if want := []string{"after"}; !reflect.DeepEqual(calls, want) {
				t.Fatalf("record calls = %#v, want %#v", calls, want)
			}
		})
	}
}

func TestSudoUserIsInheritedByChildShellAndRestored(t *testing.T) {
	var users []string
	simulator := NewBuilder().Command("record", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
		users = append(users, command.State().User())
		return &CommandResult{}, nil
	}).Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `sudo -u root sh -c 'record'; record`,
		User:   "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"root", "alice"}; !reflect.DeepEqual(users, want) {
		t.Fatalf("users = %#v, want %#v", users, want)
	}
}

func TestChangedUserIsRestoredOnEveryNestedBranch(t *testing.T) {
	observed := make(map[string]int)
	simulator := NewBuilder().Command("record", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		observed[command.State().User()+":"+argumentString(t, invocation.Args[0])]++
		return &CommandResult{}, nil
	}).Build()

	source := `sudo -u root sh -c 'if feature-gate; then record yes; else record no; fi'; record after`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source, User: "alice"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"root:yes": 1, "root:no": 1, "alice:after": 2}
	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("observed = %#v, want %#v", observed, want)
	}
}

func TestChangeUserRollsBackWhenMemoryBudgetIsExceeded(t *testing.T) {
	var observed string
	simulator := NewBuilder().
		Limits(&Limits{MaxMemoryBytes: 4096}).
		Command("change-user", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			if err := command.ChangeUser(strings.Repeat("x", 8192)); err == nil {
				t.Fatal("ChangeUser() error = nil, want memory limit error")
			}
			if got := command.State().User(); got != "alice" {
				t.Fatalf("user after failed ChangeUser = %q, want alice", got)
			}
			return &CommandResult{}, nil
		}).
		Command("record", func(_ context.Context, command *CommandContext, _ *Invocation) (*CommandResult, error) {
			observed = command.State().User()
			return &CommandResult{}, nil
		}).
		Build()

	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `change-user; record`, User: "alice"}); err != nil {
		t.Fatal(err)
	}
	if observed != "alice" {
		t.Fatalf("outer user = %q, want alice", observed)
	}
}
