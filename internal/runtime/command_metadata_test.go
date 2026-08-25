package runtime

import (
	"context"
	"testing"
)

func TestCandidateLookupExcludesUnmodifiedShellDefaults(t *testing.T) {
	command := adaptTestCommand(func(_ context.Context, state *State, _ *Invocation) (*CommandResult, error) {
		return resultForTest(state, nil, nil, 0), nil
	})
	definitions := map[string]*CommandDefinition{
		"pwd":    {Command: command},
		"base64": {Command: command, Candidate: true},
		"echo":   {Command: command, Candidate: true, UserOverride: true},
	}
	candidate := commandCandidateLookup(func(name string) *CommandDefinition { return definitions[name] })
	if candidate("pwd") || !candidate("base64") || !candidate("echo") {
		t.Fatalf("candidate defaults: pwd=%v base64=%v echo=%v", candidate("pwd"), candidate("base64"), candidate("echo"))
	}
}

func TestCandidateLookupExcludesEvaluatorOwnedControls(t *testing.T) {
	command := adaptTestCommand(func(_ context.Context, state *State, _ *Invocation) (*CommandResult, error) {
		return resultForTest(state, nil, nil, 0), nil
	})
	candidate := commandCandidateLookup(func(string) *CommandDefinition {
		return &CommandDefinition{Command: command, Candidate: true, UserOverride: true}
	})
	for _, name := range []string{"break", "continue", "return", "exit"} {
		if candidate(name) {
			t.Errorf("language control command %q is an observable handler candidate", name)
		}
	}
}

func TestCandidateLookupIncludesObservableFallback(t *testing.T) {
	command := adaptTestCommand(func(_ context.Context, state *State, _ *Invocation) (*CommandResult, error) {
		return resultForTest(state, nil, nil, 0), nil
	})
	for _, test := range []struct {
		name       string
		definition *CommandDefinition
		want       bool
	}{
		{name: "default fallback", definition: &CommandDefinition{Command: command, Fallback: true}},
		{name: "user fallback", definition: &CommandDefinition{Command: command, Fallback: true, UserOverride: true}, want: true},
		{name: "middleware fallback", definition: &CommandDefinition{Command: command, Fallback: true, Candidate: true}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := commandCandidateLookup(func(string) *CommandDefinition { return test.definition })
			if got := candidate("missing"); got != test.want {
				t.Fatalf("candidate missing = %v, want %v", got, test.want)
			}
		})
	}
}
