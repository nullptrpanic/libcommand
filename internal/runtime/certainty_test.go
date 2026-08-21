package runtime

import (
	"context"
	"slices"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func TestWordCertaintyClassifiesHostAndUnresolvedData(t *testing.T) {
	s := newState(&Request{}, defaultMaxMemoryBytes)
	s.vars.putUnknown("VALUE", expand.Variable{Set: true, Kind: expand.String})
	file := parseForTest(t, `echo "$VALUE:$RANDOM"`, "certainty.sh")
	word := file.Stmts[0].Cmd.(*syntax.CallExpr).Args[1]
	certainty := wordCertainty(s, word)
	if !certainty.hostUnknown() || !certainty.dataUnknown() {
		t.Fatalf("certainty = %v, want host and unresolved data", certainty)
	}
}

func TestUnknownOutputCertaintyRouting(t *testing.T) {
	unregistered := func(context.Context, *State, *Invocation) (*CommandResult, error) { return nil, nil }

	t.Run("unredirected", func(t *testing.T) {
		paths, _, err := evaluateForTest(context.Background(), parseForTest(t, `unknown`, "unknown-output.sh"), &Request{}, &Config{
			MaxExecutionSteps: 10, LookupCommand: lookupAllCommands(unregistered),
		})
		path := mustPath(t, paths)
		if err != nil || !path.state.stdout.unresolved || !path.state.stderr.unresolved {
			t.Fatalf("path=%#v err=%v", path, err)
		}
	})

	t.Run("redirect stdout", func(t *testing.T) {
		paths, _, err := evaluateForTest(context.Background(), parseForTest(t, `unknown >file`, "unknown-output.sh"), &Request{}, &Config{
			MaxExecutionSteps: 10, LookupCommand: lookupAllCommands(unregistered),
		})
		path := mustPath(t, paths)
		_, fileUnknown := path.state.fs.readValue("/file")
		if err != nil || path.state.stdout.unresolved || !path.state.stderr.unresolved || !fileUnknown {
			t.Fatalf("path=%#v fileUnknown=%t err=%v", path, fileUnknown, err)
		}
	})

	t.Run("redirect both and known replacement", func(t *testing.T) {
		paths, _, err := evaluateForTest(context.Background(), parseForTest(t, `unknown &>file; echo known >file`, "unknown-output.sh"), &Request{}, &Config{
			MaxExecutionSteps: 10, LookupCommand: lookupAllCommands(unregistered),
		})
		path := mustPath(t, paths)
		contents, fileUnknown := path.state.fs.readValue("/file")
		if err != nil || path.state.stdout.unresolved || path.state.stderr.unresolved || fileUnknown || string(contents) != "known\n" {
			t.Fatalf("path=%#v file=%q unknown=%t err=%v", path, contents, fileUnknown, err)
		}
	})

	t.Run("pipeline right output", func(t *testing.T) {
		paths, _, err := evaluateForTest(context.Background(), parseForTest(t, `known | unknown`, "unknown-output.sh"), &Request{}, &Config{
			MaxExecutionSteps: 10, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, invocation *Invocation) (*CommandResult, error) {
				if invocation.Name == "known" {
					return &CommandResult{Stdout: []byte("known\n")}, nil
				}
				return nil, nil
			}),
		})
		path := mustPath(t, paths)
		if err != nil || !path.state.stdout.unresolved || !path.state.stderr.unresolved {
			t.Fatalf("path=%#v err=%v", path, err)
		}
	})
}

func TestUnknownSubstitutionCertainty(t *testing.T) {
	e, s := newExecutorForTest(context.Background(), 10, &Request{}, func(context.Context, *State, *Invocation) (*CommandResult, error) {
		return nil, nil
	})
	file := parseForTest(t, `echo "$(unknown)"`, "unknown-substitution.sh")
	substitution := firstCommandSubstitution(file.Stmts[0].Cmd)
	s.pushSubstitutionFrame()
	paths, err := e.evaluateSubstitutionPaths(s, substitution)
	path := mustPath(t, paths)
	result, exists := path.state.substitution(substitution)
	if err != nil || !exists || !result.stdout.unresolved {
		t.Fatalf("result=%#v exists=%t path=%#v err=%v", result, exists, path, err)
	}

	combinedFile := parseForTest(t, `echo "$(unknown 2>&1)"`, "unknown-combined-substitution.sh")
	combinedSubstitution := firstCommandSubstitution(combinedFile.Stmts[0].Cmd)
	combinedState := newState(&Request{}, defaultMaxMemoryBytes)
	combinedState.pushSubstitutionFrame()
	paths, err = e.evaluateSubstitutionPaths(combinedState, combinedSubstitution)
	path = mustPath(t, paths)
	result, exists = path.state.substitution(combinedSubstitution)
	if err != nil || !exists || !result.stdout.unresolved || path.state.stderr.unresolved {
		t.Fatalf("combined result=%#v exists=%t path=%#v err=%v", result, exists, path, err)
	}

	inputFile := parseForTest(t, `echo "$(<input)"`, "unknown-input-substitution.sh")
	inputSubstitution := firstCommandSubstitution(inputFile.Stmts[0].Cmd)
	inputState := newState(&Request{}, defaultMaxMemoryBytes)
	if err := inputState.fs.writeValue("/input", []byte("representative\n"), false, true); err != nil {
		t.Fatal(err)
	}
	inputState.pushSubstitutionFrame()
	paths, err = e.evaluateSubstitutionPaths(inputState, inputSubstitution)
	path = mustPath(t, paths)
	result, exists = path.state.substitution(inputSubstitution)
	if err != nil || !exists || !result.stdout.unresolved || string(result.stdout.data) != "representative" {
		t.Fatalf("input result=%#v exists=%t path=%#v err=%v", result, exists, path, err)
	}
}

func TestUnknownDataAssignmentPropagation(t *testing.T) {
	script := `
plain=$(unknown)
quoted="$(unknown)"
concatenated="prefix$(unknown)suffix"
appended=known
appended+="$(unknown)"
indexed=(known "$(unknown)")
declare -A associative=([key]="$(unknown)")
copy_local() {
  local local_value="$(unknown)"
  from_local=$local_value
}
copy_local
replaced=$(unknown)
replaced=known
`
	paths, _, err := evaluateForTest(context.Background(), parseForTest(t, script, "unknown-assignment.sh"), &Request{}, &Config{
		MaxExecutionSteps: 100, LookupCommand: lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
			return nil, nil
		}),
	})
	path := mustPath(t, paths)
	if err != nil || path.status != StatusCompleted {
		t.Fatalf("path=%#v err=%v", path, err)
	}
	for _, name := range []string{"plain", "quoted", "concatenated", "appended", "indexed", "associative", "from_local"} {
		if !path.state.vars.isUnknown(name) {
			t.Errorf("%s certainty was lost: %#v", name, path.state.vars.Get(name))
		}
	}
	if path.state.vars.isUnknown("replaced") || path.state.vars.Get("replaced").String() != "known" {
		t.Fatalf("known replacement = %#v unknown=%t", path.state.vars.Get("replaced"), path.state.vars.isUnknown("replaced"))
	}
}

func TestUnknownDataArrayIndexIsUnresolvedAndAtomic(t *testing.T) {
	script := `
index=$(unknown)
values=(original)
values[$index]=mutated
`
	paths, _, err := evaluateForTest(context.Background(), parseForTest(t, script, "unknown-index.sh"), &Request{}, &Config{
		MaxExecutionSteps: 20, LookupCommand: lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
			return nil, nil
		}),
	})
	path := mustPath(t, paths)
	value := path.state.vars.Get("values")
	if err != nil || path.status != StatusUnresolved || path.state.issue == nil || !strings.Contains(path.state.issue.Error(), "unresolved command output") {
		t.Fatalf("path=%#v value=%#v err=%v", path, value, err)
	}
	if path.state.vars.isUnknown("values") || len(value.List) != 1 || value.List[0] != "original" {
		t.Fatalf("array mutated: value=%#v unknown=%t", value, path.state.vars.isUnknown("values"))
	}
}

func TestUnknownDataAssignmentBranchIsolation(t *testing.T) {
	script := `
source=$(unknown)
source=temporary record
if [[ $RANDOM ]]; then
  branch=$source
else
  branch=known
fi
`
	paths, _, err := evaluateForTest(context.Background(), parseForTest(t, script, "unknown-branch-assignment.sh"), &Request{}, &Config{
		MaxExecutionSteps: 30, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, invocation *Invocation) (*CommandResult, error) {
			if invocation.Name == "record" {
				return &CommandResult{}, nil
			}
			return nil, nil
		}),
	})
	if err != nil || len(paths) != 2 {
		t.Fatalf("paths=%#v err=%v", paths, err)
	}
	unknownBranches := 0
	for _, path := range paths {
		if path.status != StatusCompleted || !path.state.vars.isUnknown("source") {
			t.Fatalf("path=%#v", path)
		}
		if path.state.vars.isUnknown("branch") {
			unknownBranches++
		} else if path.state.vars.Get("branch").String() != "known" {
			t.Fatalf("known branch=%#v", path.state.vars.Get("branch"))
		}
	}
	if unknownBranches != 1 {
		t.Fatalf("unknown branch count=%d paths=%#v", unknownBranches, paths)
	}
}

func TestUnknownDataBooleanBranches(t *testing.T) {
	tests := []struct {
		name   string
		script string
	}{
		{"double bracket", `value=$(unknown); [[ $value ]] && record yes || record no`},
		{"double bracket pattern", `value=$(unknown); [[ known == $value ]] && record yes || record no`},
		{"test builtin", `value=$(unknown); [ "$value" = expected ] && record yes || record no`},
		{"case", `value=$(unknown); case "$value" in expected) record yes;; *) record no;; esac`},
		{"case pattern", `value=$(unknown); case known in "$value") record yes;; *) record no;; esac`},
		{"arithmetic", `value=$(unknown); (( value )) && record yes || record no`},
		{"direct substitution", `[[ $(unknown) ]] && record yes || record no`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			paths, _, err := evaluateForTest(context.Background(), parseForTest(t, test.script, "unknown-boolean.sh"), &Request{}, &Config{
				MaxExecutionSteps: 30, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, invocation *Invocation) (*CommandResult, error) {
					if invocation.Name != "record" {
						return nil, nil
					}
					calls = append(calls, argumentStrings(t, invocation)...)
					return &CommandResult{}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			requirePathStatus(t, paths, StatusCompleted)
			slices.Sort(calls)
			if !slices.Equal(calls, []string{"no", "yes"}) {
				t.Fatalf("calls=%#v paths=%#v", calls, paths)
			}
		})
	}

	var calls []string
	paths, _, err := evaluateForTest(context.Background(), parseForTest(t, `value=$(unknown); value=known; [[ $value ]] && record yes || record no`, "known-overwrite.sh"), &Request{}, &Config{
		MaxExecutionSteps: 20, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, invocation *Invocation) (*CommandResult, error) {
			if invocation.Name == "record" {
				calls = append(calls, argumentStrings(t, invocation)...)
				return &CommandResult{}, nil
			}
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	requirePathStatus(t, paths, StatusCompleted)
	if !slices.Equal(calls, []string{"yes"}) {
		t.Fatalf("known overwrite calls=%#v paths=%#v", calls, paths)
	}

	calls = nil
	paths, _, err = evaluateForTest(context.Background(), parseForTest(t, `value=$(unknown); for ((; value; )); do record body; break; done; record after`, "unknown-loop-data.sh"), &Request{}, &Config{
		MaxExecutionSteps: 30, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, invocation *Invocation) (*CommandResult, error) {
			if invocation.Name == "record" {
				calls = append(calls, argumentStrings(t, invocation)...)
				return &CommandResult{}, nil
			}
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	requirePathStatus(t, paths, StatusCompleted)
	slices.Sort(calls)
	if !slices.Equal(calls, []string{"after", "after", "body"}) {
		t.Fatalf("unknown loop calls=%#v paths=%#v", calls, paths)
	}
}

func TestUnknownDataBooleanForkConsumesExecutionStep(t *testing.T) {
	paths, executedSteps, err := evaluateForTest(context.Background(), parseForTest(t, `value=$(unknown); [[ $value ]]`, "unknown-data-budget.sh"), &Request{}, &Config{
		MaxExecutionSteps: 3, LookupCommand: lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
			return nil, nil
		}),
	})
	path := mustPath(t, paths)
	if err != nil || executedSteps != 3 || path.status != StatusIncomplete || path.state.issue == nil || !strings.Contains(path.state.issue.Error(), "maximum execution step count 3 reached") {
		t.Fatalf("path=%#v executedSteps=%d err=%v", path, executedSteps, err)
	}
}

func TestUnknownDataConcreteBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		script    string
		status    Status
		wantIssue bool
	}{
		{"command name", `value=$(unknown); $value argument`, StatusCompleted, false},
		{"function argument", `forward() { registered "$1"; }; value=$(unknown); forward "$value"`, StatusUnresolved, true},
		{"redirection path", `value=$(unknown); echo data >"$value"`, StatusCompleted, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths, _, err := evaluateForTest(context.Background(), parseForTest(t, test.script, "unknown-boundary.sh"), &Request{}, &Config{
				MaxExecutionSteps: 30,
				LookupCommand:     func(string) *CommandDefinition { return nil },
			})
			path := mustPath(t, paths)
			if err != nil || path.status != test.status || (path.state.issue != nil) != test.wantIssue {
				t.Fatalf("path=%#v err=%v", path, err)
			}
			if test.wantIssue && !strings.Contains(path.state.issue.Error(), "unresolved command output") {
				t.Fatalf("issue = %v", path.state.issue)
			}
		})
	}

	t.Run("registered handler boundary", func(t *testing.T) {
		var invocation *Invocation
		handlerLookup := lookupCommands(func(name string) bool {
			return name == "registered"
		},
			func(_ context.Context, _ *State, current *Invocation) (*CommandResult, error) {
				if current.Name == "registered" {
					invocation = current
					return &CommandResult{}, nil
				}
				return nil, nil
			})
		paths, _, err := evaluateForTest(context.Background(), parseForTest(t, `value=$(unknown); export value; unknown | registered "$value"`, "unknown-handler-boundary.sh"), &Request{}, &Config{
			MaxExecutionSteps: 30, LookupCommand: func(name string) *CommandDefinition {
				if builtin := runtimeTestBuiltin(name); builtin != nil {
					return builtin
				}
				return handlerLookup(name)
			},
		})
		path := mustPath(t, paths)
		if err != nil || path.status != StatusCompleted || path.state.issue != nil || invocation == nil {
			t.Fatalf("path=%#v invocation=%#v err=%v", path, invocation, err)
		}
		if len(invocation.Args) != 1 || *invocation.Args[0] != (Argument{Kind: ArgumentUnresolved}) {
			t.Fatalf("arguments=%#v", invocation.Args)
		}
		if invocation.Unresolved == nil || !slices.Equal(invocation.Unresolved.Env, []string{"value"}) || !invocation.Unresolved.Stdin {
			t.Fatalf("unresolved=%#v", invocation.Unresolved)
		}
		if invocation.Env["value"] != "" || invocation.Stdin != nil {
			t.Fatalf("environment=%#v stdin=%q", invocation.Env, invocation.Stdin)
		}
	})

	t.Run("mutating arithmetic", func(t *testing.T) {
		paths, _, err := evaluateForTest(context.Background(), parseForTest(t, `value=$(unknown); target=7; (( target = value ))`, "unknown-arithmetic-mutation.sh"), &Request{}, &Config{
			MaxExecutionSteps: 20, LookupCommand: lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
				return nil, nil
			}),
		})
		path := mustPath(t, paths)
		if err != nil || path.status != StatusUnresolved || path.state.vars.Get("target").String() != "7" || path.state.issue == nil || !strings.Contains(path.state.issue.Error(), "unresolved command output") {
			t.Fatalf("path=%#v err=%v", path, err)
		}
	})

	t.Run("let mutation", func(t *testing.T) {
		paths, _, err := evaluateForTest(context.Background(), parseForTest(t, `value=$(unknown); target=7; let 'target = value'`, "unknown-let-mutation.sh"), &Request{}, &Config{
			MaxExecutionSteps: 20, LookupCommand: lookupAllCommands(func(context.Context, *State, *Invocation) (*CommandResult, error) {
				return nil, nil
			}),
		})
		path := mustPath(t, paths)
		if err != nil || path.status != StatusUnresolved || path.state.vars.Get("target").String() != "7" || path.state.issue == nil || !strings.Contains(path.state.issue.Error(), "unresolved command output") {
			t.Fatalf("path=%#v err=%v", path, err)
		}
	})
}

func TestUnknownStdinInputBuiltinsRemainInPipelineChild(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		variable string
	}{
		{"read", `unknown | read value; [[ $value ]] && record yes || record no`, "value"},
		{"mapfile", `unknown | mapfile values; [[ ${#values[@]} -gt 0 ]] && record yes || record no`, "values"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			paths, _, err := evaluateForTest(context.Background(), parseForTest(t, test.script, "unknown-stdin.sh"), &Request{}, &Config{
				MaxExecutionSteps: 30, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, invocation *Invocation) (*CommandResult, error) {
					if invocation.Name == "record" {
						calls = append(calls, argumentStrings(t, invocation)...)
						return &CommandResult{}, nil
					}
					return nil, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			requirePathStatus(t, paths, StatusCompleted)
			for _, path := range paths {
				if _, exists := path.state.vars.lookup(test.variable); exists {
					t.Fatalf("%s leaked from pipeline child: path=%#v", test.variable, path)
				}
			}
			slices.Sort(calls)
			if !slices.Equal(calls, []string{"no"}) {
				t.Fatalf("calls=%#v paths=%#v", calls, paths)
			}
		})
	}

}

func TestUnknownDataParameterOperatorSafety(t *testing.T) {
	script := `
value=$(unknown)
defaulted=${value-$(probe)}
errored=${value?$(probe)}
alternate=${value+known}
length=${#value}
slice=${value:0:1}
replaced=${value/x/y}
`
	probeCalls := 0
	paths, _, err := evaluateForTest(context.Background(), parseForTest(t, script, "unknown-parameter.sh"), &Request{}, &Config{
		MaxExecutionSteps: 30, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, invocation *Invocation) (*CommandResult, error) {
			if invocation.Name == "probe" {
				probeCalls++
				return &CommandResult{Stdout: []byte("probe\n")}, nil
			}
			return nil, nil
		}),
	})
	path := mustPath(t, paths)
	if err != nil || path.status != StatusCompleted || probeCalls != 0 {
		t.Fatalf("path=%#v probeCalls=%d err=%v", path, probeCalls, err)
	}
	for _, name := range []string{"defaulted", "errored", "length", "slice", "replaced"} {
		if !path.state.vars.isUnknown(name) {
			t.Errorf("%s certainty was lost: %#v", name, path.state.vars.Get(name))
		}
	}
	if path.state.vars.isUnknown("alternate") || path.state.vars.Get("alternate").String() != "known" {
		t.Fatalf("alternate=%#v unknown=%t", path.state.vars.Get("alternate"), path.state.vars.isUnknown("alternate"))
	}

	for _, operator := range []string{":-", ":+", ":=", ":?"} {
		t.Run(operator, func(t *testing.T) {
			probeCalls := 0
			source := "value=$(unknown); result=${value" + operator + "$(probe)}"
			paths, _, err := evaluateForTest(context.Background(), parseForTest(t, source, "unknown-colon-parameter.sh"), &Request{}, &Config{
				MaxExecutionSteps: 20, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, invocation *Invocation) (*CommandResult, error) {
					if invocation.Name == "probe" {
						probeCalls++
						return &CommandResult{Stdout: []byte("probe\n")}, nil
					}
					return nil, nil
				}),
			})
			path := mustPath(t, paths)
			if err != nil || path.status != StatusUnresolved || path.state.issue == nil || !strings.Contains(path.state.issue.Error(), "unresolved command output") || probeCalls != 0 || path.state.vars.Get("result").IsSet() {
				t.Fatalf("path=%#v probeCalls=%d err=%v", path, probeCalls, err)
			}
		})
	}

	probeCalls = 0
	paths, _, err = evaluateForTest(context.Background(), parseForTest(t, `known=present; result=${known:-$(probe)}; empty=${missing:+$(probe)}`, "known-parameter.sh"), &Request{}, &Config{
		MaxExecutionSteps: 10, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, invocation *Invocation) (*CommandResult, error) {
			if invocation.Name == "probe" {
				probeCalls++
				return &CommandResult{Stdout: []byte("probe\n")}, nil
			}
			return nil, nil
		}),
	})
	path = mustPath(t, paths)
	if err != nil || path.status != StatusCompleted || probeCalls != 0 || path.state.vars.Get("result").String() != "present" || path.state.vars.Get("empty").String() != "" {
		t.Fatalf("path=%#v probeCalls=%d err=%v", path, probeCalls, err)
	}
}

func TestUnknownDataAdditionalConsumers(t *testing.T) {
	t.Run("for item cardinality is concrete", func(t *testing.T) {
		paths, _, err := evaluateForTest(context.Background(), parseForTest(t, `value=$(unknown); for item in "$value"; do record body; done`, "unknown-for-items.sh"), &Request{}, &Config{
			MaxExecutionSteps: 20,
			LookupCommand:     func(string) *CommandDefinition { return nil },
		})
		if err != nil || len(paths) != 2 {
			t.Fatalf("paths=%#v err=%v", paths, err)
		}
		unknownItems := 0
		for _, path := range paths {
			if path.status != StatusCompleted || path.state.issue != nil {
				t.Fatalf("path=%#v", path)
			}
			if path.state.vars.isUnknown("item") {
				unknownItems++
			}
		}
		if unknownItems != 1 {
			t.Fatalf("unknown item paths = %d, want one abstract iteration", unknownItems)
		}
	})

	for _, test := range []struct {
		name   string
		script string
	}{
		{"here string", `value=$(unknown); read item <<<"$value"; [[ $item ]] && record yes || record no`},
		{"here document", "value=$(unknown)\nread item <<EOF\n$value\nEOF\n[[ $item ]] && record yes || record no"},
		{"unknown file size", `unknown >file; [[ -s file ]] && record yes || record no`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			paths, _, err := evaluateForTest(context.Background(), parseForTest(t, test.script, "unknown-additional.sh"), &Request{}, &Config{
				MaxExecutionSteps: 40, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, invocation *Invocation) (*CommandResult, error) {
					if invocation.Name == "record" {
						calls = append(calls, argumentStrings(t, invocation)...)
						return &CommandResult{}, nil
					}
					return nil, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			requirePathStatus(t, paths, StatusCompleted)
			slices.Sort(calls)
			if !slices.Equal(calls, []string{"no", "yes"}) {
				t.Fatalf("calls=%#v paths=%#v", calls, paths)
			}
		})
	}

	t.Run("select explores unknown stdin", func(t *testing.T) {
		var calls []string
		paths, _, err := evaluateForTest(context.Background(), parseForTest(t, `unknown | select choice in one two; do record "$choice"; break; done`, "unknown-select-input.sh"), &Request{}, &Config{
			MaxExecutionSteps: 40, LookupCommand: lookupAllCommands(func(_ context.Context, _ *State, invocation *Invocation) (*CommandResult, error) {
				if invocation.Name == "record" {
					calls = append(calls, argumentStrings(t, invocation)...)
					return &CommandResult{}, nil
				}
				return nil, nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		requirePathStatus(t, paths, StatusCompleted)
		slices.Sort(calls)
		if !slices.Equal(calls, []string{"", "one", "two"}) {
			t.Fatalf("calls=%#v paths=%#v", calls, paths)
		}
	})

}
