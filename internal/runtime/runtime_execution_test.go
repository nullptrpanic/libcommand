package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func TestRuntimeTargetedExecutionBranches(t *testing.T) {
	for _, test := range []struct {
		name, script string
		status       Status
	}{
		{"host arithmetic forks", `((RANDOM))`, StatusCompleted},
		{"arithmetic evaluation failure", `((1 / 0))`, StatusCompleted},
		{"arithmetic budget", `((1)); ((2))`, StatusIncomplete},
		{"for condition host dependency", `for ((i=0; RANDOM; i++)); do missed; break; done`, StatusCompleted},
		{"for post host dependency", `for ((i=0; i<1; RANDOM)); do cmd value; done`, StatusUnresolved},
		{"for condition arithmetic error", `for ((i=0; 1 / 0; i++)); do missed; done`, StatusUnresolved},
		{"for post arithmetic error", `for ((i=0; i<1; 1 / 0)); do cmd value; done`, StatusUnresolved},
		{"for word host expansion", `for value in "$RANDOM"; do missed; done`, StatusUnresolved},
		{"select reaches EOF without input", `select value in one; do break; done`, StatusCompleted},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths, calls, err := runBash(t, test.script, &Request{}, func(config *Config, _ *[]*dispatchedCommand) {
				if test.name == "arithmetic budget" {
					config.MaxExecutionSteps = 1
				}
			})
			if err != nil {
				t.Fatalf("paths=%#v calls=%#v err=%v", paths, calls, err)
			}
			requirePathStatus(t, paths, test.status)
			if test.name == "arithmetic evaluation failure" && paths[0].state.exitStatus.Value != 1 {
				t.Fatalf("arithmetic failure exit code = %d, want 1", paths[0].state.exitStatus.Value)
			}
			if test.name == "host arithmetic forks" {
				if len(paths) != 2 {
					t.Fatalf("arithmetic paths = %#v", paths)
				}
			}
			if test.name == "for condition host dependency" && (len(paths) != 2 || len(calls) != 1) {
				t.Fatalf("for condition paths=%#v calls=%#v", paths, calls)
			}
			if test.name == "select reaches EOF without input" && len(paths) != 1 {
				t.Fatalf("select paths=%#v", paths)
			}
			if test.name == "arithmetic budget" && (len(paths) != 1 || paths[0].state.issue == nil) {
				t.Fatalf("budget paths = %#v", paths)
			}
		})
	}

	t.Run("pipeline routes stderr only through pipe all", func(t *testing.T) {
		file := parseForTest(t, `left | right; left |& all`, "pipe.sh")
		var received []string
		err := Execute(context.Background(), file, &Request{}, &Config{MaxExecutionSteps: 10, LookupCommand: lookupAllCommands(func(_ context.Context, state *State, command *Invocation) (*CommandResult, error) {
			switch command.Name {
			case "left":
				return resultForTest(state, nil, []byte("left-stderr\n"), 0), nil
			case "right", "all":
				received = append(received, command.Name+":"+string(command.Stdin))
				return resultForTest(state, nil, nil, 0), nil
			default:
				t.Fatalf("unexpected command %#v", command)
				return nil, nil
			}
		})})
		if err != nil || !reflect.DeepEqual(received, []string{"right:", "all:left-stderr\n"}) {
			t.Fatalf("received=%#v err=%v", received, err)
		}
	})

	t.Run("pipeline propagates handler error and stop", func(t *testing.T) {
		results := []struct {
			name    string
			stop    bool
			want    Status
			wantErr bool
		}{
			{"error", false, StatusIncomplete, true}, {"stop", true, StatusTerminated, false},
		}
		for index := range results {
			result := &results[index]
			t.Run(result.name, func(t *testing.T) {
				paths, _, err := runBash(t, `left | right`, &Request{}, func(config *Config, _ *[]*dispatchedCommand) {
					config.LookupCommand = lookupAllCommands(func(_ context.Context, state *State, _ *Invocation) (*CommandResult, error) {
						if result.wantErr {
							return nil, errors.New("left failed")
						}
						if result.stop {
							return stoppedResultForTest(state, nil, nil, 0), nil
						}
						return resultForTest(state, nil, nil, 0), nil
					})
				})
				if (err != nil) != result.wantErr {
					t.Fatalf("paths=%#v err=%v", paths, err)
				}
				requirePathStatus(t, paths, result.want)
			})
		}
	})
}

func TestDispatchBehaviorFacts(t *testing.T) {
	t.Run("nil existence callback dispatches external", func(t *testing.T) {
		count := 0
		err := Execute(context.Background(), parseForTest(t, `external`, "facts.sh"), &Request{}, &Config{
			MaxExecutionSteps: 3, LookupCommand: lookupAllCommands(func(_ context.Context, state *State, _ *Invocation) (*CommandResult, error) {
				count++
				return resultForTest(state, nil, nil, 0), nil
			}),
		})
		if err != nil || count != 1 {
			t.Fatalf("count=%d err=%v", count, err)
		}
	})
	t.Run("known unregistered command skips dispatch", func(t *testing.T) {
		count := 0
		err := Execute(context.Background(), parseForTest(t, `external`, "facts.sh"), &Request{}, &Config{
			MaxExecutionSteps: 3, LookupCommand: lookupCommands(func(string) bool { return false },
				func(_ context.Context, state *State, _ *Invocation) (*CommandResult, error) {
					count++
					return resultForTest(state, nil, nil, 0), nil
				}),
		})
		if err != nil || count != 0 {
			t.Fatalf("count=%d err=%v", count, err)
		}
	})
	t.Run("unregistered dispatcher records empty result", func(t *testing.T) {
		err := Execute(context.Background(), parseForTest(t, `external arg`, "facts.sh"), &Request{}, &Config{MaxExecutionSteps: 3, LookupCommand: lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
			return nil, nil
		})})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	t.Run("stop suppresses later dispatch", func(t *testing.T) {
		count := 0
		var name string
		err := Execute(context.Background(), parseForTest(t, `first; second`, "facts.sh"), &Request{}, &Config{MaxExecutionSteps: 3, LookupCommand: lookupAllCommands(func(_ context.Context, state *State, command *Invocation) (*CommandResult, error) {
			count++
			name = command.Name
			return stoppedResultForTest(state, []byte(command.Name), nil, 0), nil
		})})
		if err != nil || count != 1 || name != "first" {
			t.Fatalf("name=%q count=%d err=%v", name, count, err)
		}
	})
	t.Run("handler error records no result and includes diagnostic", func(t *testing.T) {
		err := Execute(context.Background(), parseForTest(t, `broken`, "facts.sh"), &Request{}, &Config{MaxExecutionSteps: 3, LookupCommand: lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
			return nil, errors.New("handler exploded")
		})})
		if err == nil || !strings.Contains(err.Error(), "handler exploded") {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	t.Run("budget diagnostic includes source", func(t *testing.T) {
		err := Execute(context.Background(), parseForTest(t, `one; two`, "facts.sh"), &Request{}, &Config{MaxExecutionSteps: 1, LookupCommand: lookupAllCommands(noOpDispatch)})
		if err == nil || !strings.Contains(err.Error(), "maximum execution step count 1 reached") || !strings.Contains(err.Error(), "at 1:") {
			t.Fatalf("Execute() error = %v", err)
		}
	})
}

func TestRedirectionSubstitutionAndBuiltinEffects(t *testing.T) {
	t.Run("virtual files preserve append stderr and merged streams", func(t *testing.T) {
		file := parseForTest(t, `echo seed > file; echo append >> file; emit 2> err; emit > both 2>&1; inspect "$(<file)" "$(<err)" "$(<both)"; read answer <<EOF
heredoc value
EOF
inspect "$answer" <<< trailing`, "files.sh")
		var inspected [][]string
		err := Execute(context.Background(), file, &Request{}, &Config{MaxExecutionSteps: 30, LookupCommand: lookupAllCommands(func(_ context.Context, state *State, command *Invocation) (*CommandResult, error) {
			switch command.Name {
			case "emit":
				return resultForTest(state, []byte("out"), []byte("err"), 0), nil
			case "inspect":
				inspected = append(inspected, argumentStrings(t, command))
				if len(inspected) == 2 && string(command.Stdin) != "trailing\n" {
					t.Fatalf("here string stdin = %q", command.Stdin)
				}
				return resultForTest(state, nil, nil, 0), nil
			default:
				return resultForTest(state, nil, nil, 0), nil
			}
		})})
		if err != nil || !reflect.DeepEqual(inspected, [][]string{{"seed\nappend", "err", "outerr"}, {"heredoc value"}}) {
			t.Fatalf("inspected=%#v err=%v", inspected, err)
		}
	})
	t.Run("process substitution is isolated", func(t *testing.T) {
		paths, calls, err := runBash(t, `cmd < <(echo data)`, &Request{}, nil)
		path := mustPath(t, paths)
		if err != nil || path.status != StatusCompleted || len(calls) != 1 || calls[0].stdin != "data\n" || path.state.issue != nil {
			t.Fatalf("paths=%#v calls=%#v err=%v", paths, calls, err)
		}
	})
}

func TestDirectExecutorErrorPathsAndScopes(t *testing.T) {
	t.Run("function assignment failure restores scope", func(t *testing.T) {
		e, s := newNoOpExecutor(context.Background(), 20, &Request{Env: map[string]string{"outer": "value"}})
		file := parseForTest(t, `f() { local local_only=value; :; }; bad=$RANDOM f`, "direct.sh")
		if _, err := e.evaluateStatement(s, file.Stmts[0]); err != nil {
			t.Fatal(err)
		}
		paths, err := e.evaluateStatement(s, file.Stmts[1])
		if err != nil || paths[0].status != StatusUnresolved || len(s.localScopes) != 0 || s.funcDepth != 0 || s.vars.Get("bad").IsSet() || s.vars.Get("outer").String() != "value" {
			t.Fatalf("paths=%#v state=%#v err=%v", paths, s, err)
		}
	})
	t.Run("redirect plans model arbitrary and unresolved descriptors", func(t *testing.T) {
		for _, script := range []string{`cmd <&1`, `cmd 1<<<value`, `cmd >$RANDOM`} {
			e, s := newNoOpExecutor(context.Background(), 20, &Request{Env: map[string]string{"outer": "value"}})
			file := parseForTest(t, script, "direct.sh")
			plan, err := e.prepareRedirections(s, file.Stmts[0].Redirs)
			if err != nil || plan == nil {
				t.Fatalf("%s: plan=%#v err=%v", script, plan, err)
			}
		}
	})
	t.Run("input substitution distinguishes non-input and expansion failure", func(t *testing.T) {
		e, s := newNoOpExecutor(context.Background(), 20, &Request{Env: map[string]string{"outer": "value"}})
		for _, test := range []struct {
			script  string
			ok      bool
			wantErr bool
		}{
			{`echo "$(echo value)"`, false, false}, {`echo "$(<$RANDOM)"`, true, false},
		} {
			file := parseForTest(t, test.script, "direct.sh")
			sub := firstCommandSubstitution(file.Stmts[0].Cmd)
			_, ok, err := e.inputOnlySubstitution(s, sub)
			if ok != test.ok || (err != nil) != test.wantErr {
				t.Fatalf("%s: ok=%t err=%v", test.script, ok, err)
			}
		}
	})
	t.Run("case known no match and invalid expansions", func(t *testing.T) {
		for _, test := range []struct {
			script    string
			status    Status
			callCount int
		}{
			{`case value in other) missed;; esac`, StatusCompleted, 0}, {`case value in "$RANDOM") missed;; esac`, StatusCompleted, 1},
		} {
			paths, calls, err := runBash(t, test.script, &Request{}, nil)
			if err != nil || len(calls) != test.callCount {
				t.Fatalf("paths=%#v calls=%#v err=%v", paths, calls, err)
			}
			requirePathStatus(t, paths, test.status)
		}
	})
	t.Run("associative indices support literal expansion and arithmetic", func(t *testing.T) {
		e, s := newNoOpExecutor(context.Background(), 20, &Request{Env: map[string]string{"outer": "value"}})
		s.vars.put("key", expand.Variable{Set: true, Kind: expand.String, Str: "expanded"})
		file := parseForTest(t, `m[key]=literal; m[$key]=expanded; m[$((1+2))]=numeric`, "direct.sh")
		for _, statement := range file.Stmts {
			if err := e.applyAssignments(s, statement.Cmd.(*syntax.CallExpr).Assigns, expand.Associative, false); err != nil {
				t.Fatal(err)
			}
		}
		if got := s.vars.Get("m").Map; got["key"] != "literal" || got["expanded"] != "expanded" || got["3"] != "numeric" {
			t.Fatalf("map=%#v", got)
		}
	})
}

func TestControlFlowEdgeBehavior(t *testing.T) {
	for _, test := range []*struct {
		name, script string
		status       Status
		calls        []string
	}{
		{"while break", `while true; do cmd first; break; cmd missed; done; cmd after`, StatusCompleted, []string{"cmd", "cmd"}},
		{"for return exits function", `f() { for x in one two; do cmd "$x"; return 4; done; cmd missed; }; f; cmd after`, StatusCompleted, []string{"cmd", "cmd"}},
		{"local outside reports shell failure", `local x=value; cmd after`, StatusCompleted, []string{"cmd"}},
		{"empty expansion completes without invoking a command", `$EMPTY`, StatusCompleted, []string{}},
		{"unsupported command node is unresolved", `coproc cmd value`, StatusUnresolved, []string{}},
		{"test invalid regexp fails", `[[ value =~ [ ]]`, StatusCompleted, []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths, calls, err := runBash(t, test.script, &Request{}, nil)
			if err != nil {
				t.Fatalf("paths=%#v calls=%#v err=%v", paths, calls, err)
			}
			requirePathStatus(t, paths, test.status)
			got := make([]string, len(calls))
			for i := range calls {
				got[i] = calls[i].name
			}
			if !reflect.DeepEqual(got, test.calls) {
				t.Fatalf("calls=%#v want=%#v", calls, test.calls)
			}
			if test.name == "test invalid regexp fails" && (len(paths) != 1 || paths[0].state.exitStatus.Value != 2) {
				t.Fatalf("test paths=%#v", paths)
			}
		})
	}

	t.Run("loop continuation consumes each signal", func(t *testing.T) {
		e := &ExecutionContext{}
		for _, test := range []struct {
			signal            controlSignal
			active, completed int
		}{
			{signalNone, 1, 0}, {signalContinue, 1, 0}, {signalBreak, 0, 1}, {signalReturn, 0, 1},
		} {
			active, completed := []*pathResult{}, []*pathResult{}
			e.collectLoopContinuation(&active, &completed, &pathResult{state: &State{signal: test.signal}, status: StatusCompleted})
			if len(active) != test.active || len(completed) != test.completed {
				t.Fatalf("signal %v active=%#v completed=%#v", test.signal, active, completed)
			}
		}
	})
}
