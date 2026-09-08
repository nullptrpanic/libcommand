package libcommand

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRepositoryScript03DiscoversEveryLarkCommand(t *testing.T) {
	want := []string{
		"oc_de31d3685378ddb6f5b2978844a81d5e",
		"oc_e1c0b2c6c7df9f1c6989e2f89238e745",
		"oc_dbe3aac05d8b89603dfbf45685032fb3",
		"oc_0335bbde52f298dce06ab677236a75b4",
		"oc_aab14882d79f28deabd20c3456682609",
		"oc_115c33eab21b04d1bb2ed7c76376c08c",
		"oc_af50e5e50b53b953f2ff28cd25ec3683",
	}
	got := repositoryScriptChatIDs(t, "03-scan-remaining-groups.sh")
	for _, chatID := range want {
		if !slices.Contains(got, chatID) {
			t.Fatalf("chat id %q was not discovered; got %#v", chatID, got)
		}
	}
}

func TestRepositoryScript06DiscoversLarkCommand(t *testing.T) {
	want := []string{"oc_1c998d15aa01f540a61d5767cbd299eb"}
	got := repositoryScriptChatIDs(t, "06-check-chat-messages.sh")
	for _, chatID := range want {
		if !slices.Contains(got, chatID) {
			t.Fatalf("chat id %q was not discovered; got %#v", chatID, got)
		}
	}
}

func TestRepositoryScript14ContinuesAfterUnknownEval(t *testing.T) {
	source := repositoryScriptSource(t, "14-dynamic-source-boundary.sh")
	var calls []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		arguments := argumentStrings(t, invocation)
		if len(arguments) < 2 {
			t.Fatalf("arguments = %#v", arguments)
		}
		calls = append(calls, arguments[1])
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"known-source", "unreachable"}; !slices.Equal(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func repositoryScriptChatIDs(t testing.TB, name string) []string {
	t.Helper()
	source := repositoryScriptSource(t, name)
	var chatIDs []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
		arguments := argumentStrings(t, invocation)
		index := slices.Index(arguments, "--chat-id")
		if index < 0 || index+1 >= len(arguments) {
			t.Fatalf("arguments = %#v", arguments)
		}
		chatIDs = append(chatIDs, arguments[index+1])
		return commandResultForTest(command, nil, nil, 0), nil
	})
	if err := simulator.Simulate(context.Background(), &SimulationRequest{Source: source}); err != nil {
		t.Fatal(err)
	}
	return chatIDs
}

func repositoryScriptSource(t testing.TB, name string) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata", "scripts", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(source)
}
