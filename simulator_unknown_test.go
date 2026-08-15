package libcommand

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestSimulatorIgnoresUnregisteredCommands(t *testing.T) {
	callbacks := 0
	simulator := mustBuildSimulator(t, "lark-cli", countInvocations(&callbacks))
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: `curl https://sentinel.invalid; lark-cli ok`}); err != nil {
		t.Fatal(err)
	}
	if callbacks != 1 {
		t.Fatalf("callbacks = %d", callbacks)
	}
}

func TestSimulatorDiscoversHandlerInUnknownWordLoop(t *testing.T) {
	const chatID = "oc_28ebbd1168a2173f48bb23364a2d88fe"
	source := repositoryScriptSource(t, "08-send-30-chat-messages.sh")

	var calls [][]*Argument
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, append([]*Argument(nil), invocation.Args...))
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 30 {
		t.Fatalf("calls = %d, want 30: %#v", len(calls), calls)
	}
	wantTexts := map[string]int{
		"评测任务已启动":      10,
		"请各成员确认执行计划":   10,
		"请在今天下班前反馈风险项": 10,
	}
	gotTexts := make(map[string]int, len(wantTexts))
	for _, arguments := range calls {
		values := argumentStrings(t, &Invocation{Args: arguments})
		chatIndex := slices.Index(values, "--chat-id")
		textIndex := slices.Index(values, "--text")
		if chatIndex < 0 || chatIndex+1 >= len(values) || values[chatIndex+1] != chatID {
			t.Fatalf("chat id arguments = %#v", values)
		}
		if textIndex < 0 || textIndex+1 >= len(values) {
			t.Fatalf("text arguments = %#v", values)
		}
		text := values[textIndex+1]
		if _, expected := wantTexts[text]; !expected {
			t.Fatalf("unexpected message %q; calls = %#v", text, calls)
		}
		gotTexts[text]++
	}
	for text, want := range wantTexts {
		if got := gotTexts[text]; got != want {
			t.Fatalf("message %q calls = %d, want %d; calls = %#v", text, got, want, calls)
		}
	}
}

func TestSimulatorDiscoversIndirectHandlersInUnknownWordLoops(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "function",
			source: `f() { lark-cli via-function; }; for value in "$(unknown-command)"; do f; done`,
			want:   "via-function",
		},
		{
			name:   "env",
			source: `for value in "$(unknown-command)"; do env lark-cli via-env; done`,
			want:   "via-env",
		},
		{
			name:   "command",
			source: `for value in "$(unknown-command)"; do command lark-cli via-command; done`,
			want:   "via-command",
		},
		{
			name:   "shell",
			source: `for value in "$(unknown-command)"; do bash -c 'lark-cli via-shell'; done`,
			want:   "via-shell",
		},
		{
			name:   "eval",
			source: `for value in "$(unknown-command)"; do eval 'lark-cli via-eval'; done`,
			want:   "via-eval",
		},
		{
			name:   "source",
			source: `printf 'lark-cli via-source\n' >/script; for value in "$(unknown-command)"; do source /script; done`,
			want:   "via-source",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(calls, []string{test.want}) {
				t.Fatalf("calls = %#v, want %#v", calls, []string{test.want})
			}
		})
	}
}

func TestSimulatorDiscoversIndirectHandlerAfterUnknownDirectory(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "function", source: `f() { lark-cli via-function; }; cd /host-only && f`},
		{name: "env", source: `cd /host-only && env lark-cli via-env`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			simulator := mustBuildSimulator(t, "lark-cli", countInvocations(&calls))
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want 1", calls)
			}
		})
	}
}

func TestSimulatorUserSeqOverridesBuiltin(t *testing.T) {
	seqCalls := 0
	var values []string
	simulator := NewBuilder().
		Command("seq", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
			seqCalls++
			if got := argumentStrings(t, invocation); !slices.Equal(got, []string{"1", "10"}) {
				t.Fatalf("seq arguments = %#v", got)
			}
			return &CommandResult{Stdout: []byte("override\n")}, nil
		}).
		Command("lark-cli", recordFirstArgument(t, &values)).
		Build()
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `for value in $(seq 1 10); do lark-cli "$value"; done`,
	}); err != nil {
		t.Fatal(err)
	}
	if seqCalls != 1 || !slices.Equal(values, []string{"override"}) {
		t.Fatalf("seq calls = %d, values = %#v", seqCalls, values)
	}
}

func TestSimulatorKeepsUnresolvedDefaultSeqOpen(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "direct", source: `value=$(unknown-command); for item in $(seq "$value"); do lark-cli "$item"; done`},
		{name: "command", source: `value=$(unknown-command); for item in $(command seq "$value"); do lark-cli "$item"; done`},
		{name: "env", source: `value=$(unknown-command); for item in $(env seq "$value"); do lark-cli "$item"; done`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls [][]*Argument
			simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
				calls = append(calls, append([]*Argument(nil), invocation.Args...))
				return &CommandResult{}, nil
			})
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || len(calls[0]) != 1 || *calls[0][0] != (Argument{Kind: ArgumentUnresolved}) {
				t.Fatalf("calls = %#v", calls)
			}
		})
	}
}

func TestSimulatorDiscoversBuiltinSeqThroughCommandAndEnv(t *testing.T) {
	var calls []*Argument
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		if len(invocation.Args) != 1 {
			t.Fatalf("arguments = %#v", invocation.Args)
		}
		calls = append(calls, invocation.Args[0])
		return &CommandResult{}, nil
	})
	source := `command -v seq >/dev/null && lark-cli found
for item in $(command seq 2); do lark-cli "command-$item"; done
for item in $(env seq 2); do lark-cli "env-$item"; done`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	want := []*Argument{
		{Kind: ArgumentString, Value: "found"},
		{Kind: ArgumentString, Value: "command-1"},
		{Kind: ArgumentString, Value: "command-2"},
		{Kind: ArgumentString, Value: "env-1"},
		{Kind: ArgumentString, Value: "env-2"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorMarksUnknownHandlerArgument(t *testing.T) {
	var calls [][]*Argument
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, append([]*Argument(nil), invocation.Args...))
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `for i in $(unknown); do lark-cli "$i"; done`,
	}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(calls[0]) != 1 || *calls[0][0] != (Argument{Kind: ArgumentUnresolved}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSimulatorDiscoversDynamicHandlerInUnknownWordLoop(t *testing.T) {
	var calls [][]*Argument
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, append([]*Argument(nil), invocation.Args...))
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `command_name=lark-cli; for i in $(unknown); do "$command_name" "$i"; done`,
	}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(calls[0]) != 1 || *calls[0][0] != (Argument{Kind: ArgumentUnresolved}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSimulatorLazilyExploresUnregisteredCommandStatus(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{"unused status stays single path", `unknown-command; lark-cli after`, []string{"after"}},
		{"known success overwrites unknown", `unknown-command; true; if [[ $? -eq 0 ]]; then lark-cli success; else lark-cli failure; fi`, []string{"success"}},
		{"known failure overwrites unknown", `unknown-command; false; if [[ $? -eq 0 ]]; then lark-cli success; else lark-cli failure; fi`, []string{"failure"}},
		{"if condition", `if unknown-command; then lark-cli success; else lark-cli failure; fi`, []string{"success", "failure"}},
		{"logical and", `unknown-command && lark-cli success; lark-cli after`, []string{"success", "after", "after"}},
		{"logical or", `unknown-command || lark-cli failure; lark-cli after`, []string{"failure", "after", "after"}},
		{"negated condition", `if ! unknown-command; then lark-cli failure; else lark-cli success; fi`, []string{"failure", "success"}},
		{"special exit parameter", `unknown-command; if [[ $? -eq 17 ]]; then lark-cli matched; else lark-cli unmatched; fi`, []string{"matched", "unmatched"}},
		{"command substitution status", `value=$(unknown-command); if [[ $? -eq 0 ]]; then lark-cli success; else lark-cli failure; fi`, []string{"success", "failure"}},
		{"function return preserves unknown", `f() { unknown-command; return; }; if f; then lark-cli success; else lark-cli failure; fi`, []string{"success", "failure"}},
		{"invalid return overwrites unknown", `unknown-command; return; if [[ $? -eq 0 ]]; then lark-cli success; else lark-cli failure; fi`, []string{"failure"}},
		{"pipeline status", `if true | unknown-command; then lark-cli success; else lark-cli failure; fi`, []string{"success", "failure"}},
		{"while condition", `while unknown-command; do lark-cli body; break; done; lark-cli after`, []string{"body", "after", "after"}},
		{"until condition", `until unknown-command; do lark-cli body; break; done; lark-cli after`, []string{"body", "after", "after"}},
		{"case exit parameter", `unknown-command; case $? in 0) lark-cli zero;; 17) lark-cli seventeen;; *) lark-cli other;; esac`, []string{"zero", "seventeen", "other"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireFirstArguments(t, test.source, test.want)
		})
	}
}

func TestSimulatorExploresUnknownBranches(t *testing.T) {
	var args []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		args = append(args, argumentStrings(t, invocation)...)
		return &CommandResult{}, nil
	})
	source := `if [[ $RANDOM -gt 10 ]]; then lark-cli yes; else lark-cli no; fi
case "$RANDOM" in one) lark-cli one;; two) lark-cli two;; esac`
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"yes", "no", "one", "two", "one", "two"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestSimulatorExploresHostDependentSpecialParameters(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "shell pid", source: `if [[ $$ ]]; then lark-cli yes; else lark-cli no; fi`},
		{name: "background pid", source: `true & if [[ $! ]]; then lark-cli yes; else lark-cli no; fi`},
		{name: "background pid after wait", source: `true & wait; if [[ $! ]]; then lark-cli yes; else lark-cli no; fi`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			slices.Sort(calls)
			if want := []string{"no", "yes"}; !slices.Equal(calls, want) {
				t.Fatalf("calls = %#v, want %#v", calls, want)
			}
		})
	}
}

func TestSimulatorExploresUnknownTestBuiltinBranches(t *testing.T) {
	source := `if [ "$RANDOM" -gt 1 ]; then lark-cli yes; else lark-cli no; fi`
	requireFirstArguments(t, source, []string{"yes", "no"})
}

func TestSimulatorExploresUnknownCommandOutputData(t *testing.T) {
	tests := []string{
		`value=$(unknown-command); [[ $value ]] && lark-cli yes || lark-cli no`,
		`value=$(unknown-command); [ "$value" = expected ] && lark-cli yes || lark-cli no`,
		`value=$(unknown-command); case "$value" in expected) lark-cli yes;; *) lark-cli no;; esac`,
		`value=$(unknown-command); (( value )) && lark-cli yes || lark-cli no`,
	}
	for _, source := range tests {
		var calls []string
		simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
		if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
			t.Fatalf("Simulate(%q): %v", source, err)
		}
		slices.Sort(calls)
		if !slices.Equal(calls, []string{"no", "yes"}) {
			t.Fatalf("Simulate(%q) calls = %#v", source, calls)
		}
	}
}

func TestSimulatorIsolatesPipelineInputBuiltinVariables(t *testing.T) {
	for _, source := range []string{
		`unknown-command | read value; [[ $value ]] && lark-cli yes || lark-cli no`,
		`unknown-command | mapfile values; [[ ${#values[@]} -gt 0 ]] && lark-cli yes || lark-cli no`,
	} {
		requireFirstArguments(t, source, []string{"no"})
	}
}

func TestSimulatorRejectsUnknownDataAtConcreteBoundaries(t *testing.T) {
	tests := []string{
		`value=$(unknown-command); result=${value:-$(lark-cli probe)}`,
		`value=$(unknown-command); result=${value:+$(lark-cli probe)}`,
		`value=$(unknown-command); result=${value:=$(lark-cli probe)}`,
		`value=$(unknown-command); result=${value:?$(lark-cli probe)}`,
		`value=$(unknown-command); [[ ${value:-$(lark-cli probe)} ]]`,
		`value=$(unknown-command); case ${value:-$(lark-cli probe)} in *) :;; esac`,
	}
	for _, source := range tests {
		calls := 0
		simulator := mustBuildSimulator(t, "lark-cli", countInvocations(&calls))
		err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source})
		if err == nil || !strings.Contains(err.Error(), "unresolved command output") || calls != 0 {
			t.Fatalf("Simulate(%q) error = %v, calls = %d", source, err, calls)
		}
	}
}

func TestSimulatorMarksUnknownInvocationFields(t *testing.T) {
	var got *Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = &Invocation{
			Name:       invocation.Name,
			Args:       append([]*Argument(nil), invocation.Args...),
			Env:        maps.Clone(invocation.Env),
			Dir:        invocation.Dir,
			Stdin:      append([]byte(nil), invocation.Stdin...),
			Unresolved: invocation.Unresolved,
		}
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `value=$(unknown-command); export value; unknown-command | lark-cli "$value"`,
	}); err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Args) != 1 || *got.Args[0] != (Argument{Kind: ArgumentUnresolved}) {
		t.Fatalf("invocation = %#v", got)
	}
	if got.Unresolved == nil || !slices.Equal(got.Unresolved.Env, []string{"value"}) || !got.Unresolved.Stdin {
		t.Fatalf("unresolved fields = %#v", got.Unresolved)
	}
	if got.Env["value"] != "" || got.Stdin != nil {
		t.Fatalf("concrete unknown fields = env %#v, stdin %q", got.Env, got.Stdin)
	}
}

func TestSimulatorMarksUnknownDirectory(t *testing.T) {
	var got *Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = invocation
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `cd /unspecified && lark-cli run`,
	}); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Dir != "" || got.Unresolved == nil || !got.Unresolved.Dir {
		t.Fatalf("invocation = %#v", got)
	}
	if got.Env["PWD"] != "" || !slices.Contains(got.Unresolved.Env, "PWD") {
		t.Fatalf("environment = %#v, unresolved = %#v", got.Env, got.Unresolved)
	}
}

func TestSimulatorPropagatesUnknownBuiltinOutput(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{name: "echo", command: `echo "$value"`},
		{name: "printf", command: `printf '%s' "$value"`},
		{name: "command echo", command: `command echo "$value"`},
		{name: "builtin printf", command: `builtin printf '%s' "$value"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got *Invocation
			simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
				got = invocation
				return &CommandResult{}, nil
			})
			source := `value=$(unknown-command); ` + test.command + ` | lark-cli consume`
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
				t.Fatal(err)
			}
			if got == nil || got.Unresolved == nil || !got.Unresolved.Stdin || got.Stdin != nil {
				t.Fatalf("invocation = %#v", got)
			}
		})
	}
}

func TestSimulatorCommandBuiltinForwardsUnknownArguments(t *testing.T) {
	var got *Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = invocation
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `value=$(unknown-command); command lark-cli "$value"`,
	}); err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Args) != 1 || *got.Args[0] != (Argument{Kind: ArgumentUnresolved}) {
		t.Fatalf("invocation = %#v", got)
	}
}

func TestSimulatorEnvForwardsUnknownArguments(t *testing.T) {
	var got *Invocation
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = invocation
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `value=$(unknown-command); env -u DROP TOKEN=known lark-cli "$value"`,
		Env:    map[string]string{"DROP": "drop", "KEEP": "keep"},
	}); err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Args) != 1 || *got.Args[0] != (Argument{Kind: ArgumentUnresolved}) {
		t.Fatalf("invocation = %#v", got)
	}
	if got.Env["TOKEN"] != "known" || got.Env["KEEP"] != "keep" {
		t.Fatalf("environment = %#v", got.Env)
	}
	if _, exists := got.Env["DROP"]; exists {
		t.Fatalf("environment contains DROP: %#v", got.Env)
	}
}

func TestSimulatorExecForwardsUnknownArgumentsAndTerminates(t *testing.T) {
	var calls [][]*Argument
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		calls = append(calls, append([]*Argument(nil), invocation.Args...))
		return &CommandResult{}, nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `value=$(unknown-command); exec lark-cli "$value"; lark-cli after`,
	}); err != nil {
		t.Fatal(err)
	}
	want := [][]*Argument{{{Kind: ArgumentUnresolved}}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSimulatorFailsStatefulBuiltinsWithUnknownArguments(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{
			name:   "shift",
			source: `value=$(unknown-command); if shift "$value"; then lark-cli success; else lark-cli failure; fi`,
		},
		{
			name:   "let",
			source: `value=$(unknown-command); if let "$value"; then lark-cli success; else lark-cli failure; fi`,
		},
		{
			name:   "printf variable",
			source: `value=$(unknown-command); x=known; if printf -v x '%s' "$value"; then lark-cli success; else lark-cli failure; fi; lark-cli "$x"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			simulator := mustBuildSimulator(t, "lark-cli", recordFirstArgument(t, &calls))
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: test.source}); err != nil {
				t.Fatal(err)
			}
			want := []string{"failure"}
			if test.name == "printf variable" {
				want = append(want, "known")
			}
			if !slices.Equal(calls, want) {
				t.Fatalf("calls = %#v, want %#v", calls, want)
			}
		})
	}
}

func TestSimulatorDoesNotRejectUnknownBuiltinArguments(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{name: "ordinary builtin", command: `pwd "$value"`},
		{name: "cd", command: `cd "$value"`},
		{name: "command wrapper", command: `command pwd "$value"`},
		{name: "builtin wrapper", command: `builtin pwd "$value"`},
		{name: "eval", command: `eval "$value"`},
		{name: "source", command: `source "$value"`},
		{name: "declaration", command: `export "$value"`},
		{name: "let", command: `let "$value"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			simulator := mustBuildSimulator(t, "lark-cli", func(context.Context, *CommandContext, *Invocation) (*CommandResult, error) {
				return &CommandResult{}, nil
			})
			source := `value=$(unknown-command); ` + test.command + `; lark-cli after`
			if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSimulatorLetsKnownUnregisteredCommandsConsumeUnknownData(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "argument", source: `value=$(unknown-one); unknown-two "$value"; lark-cli after`},
		{name: "command wrapper argument", source: `value=$(unknown-one); command unknown-two "$value"; lark-cli after`},
		{name: "env wrapper argument", source: `value=$(unknown-one); env unknown-two "$value"; lark-cli after`},
		{name: "stdin", source: `unknown-one | unknown-two; lark-cli after`},
		{name: "environment", source: `value=$(unknown-one); export value; unknown-two; unset value; lark-cli after`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireFirstArguments(t, test.source, []string{"after"})
		})
	}
}

func TestSimulatorExploresUnknownCStyleLoopCondition(t *testing.T) {
	source := `for ((i=0; RANDOM; i++)); do lark-cli body; break; done; lark-cli after`
	requireFirstArguments(t, source, []string{"body", "after", "after"})
}
