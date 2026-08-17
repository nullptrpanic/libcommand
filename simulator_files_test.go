package libcommand

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestSimulatorLoadsInitialFilesRelativeToWorkingDirectory(t *testing.T) {
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentStrings(t, invocation)
		return &CommandResult{}, nil
	})

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `source ./config.sh
source /shared/global.sh
lark-cli "$PWD" "$TOKEN" "$GLOBAL" "$(<data/value.txt)"`,
		Files: map[string][]byte{
			"config.sh":         []byte("TOKEN=relative\n"),
			"data/value.txt":    []byte("payload\n"),
			"/shared/global.sh": []byte("GLOBAL=absolute\n"),
		},
		WorkingDir: "/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/workspace", "relative", "absolute", "payload"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSimulatorDefaultsInitialFilesToRootWorkingDirectory(t *testing.T) {
	var got []string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = argumentStrings(t, invocation)
		return &CommandResult{}, nil
	})

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source: `source config.sh; lark-cli "$PWD" "$VALUE"`,
		Files:  map[string][]byte{"config.sh": []byte("VALUE=loaded\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/", "loaded"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSimulatorResolvesRelativeWorkingDirectoryFromRoot(t *testing.T) {
	var got string
	simulator := mustBuildSimulator(t, "lark-cli", func(_ context.Context, _ *CommandContext, invocation *Invocation) (*CommandResult, error) {
		got = invocation.Dir
		return &CommandResult{}, nil
	})

	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Source:     `lark-cli`,
		WorkingDir: "workspace/scripts",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/workspace/scripts" {
		t.Fatalf("working directory = %q, want /workspace/scripts", got)
	}
}

func TestSimulatorDoesNotMutateInitialFiles(t *testing.T) {
	request := &SimulationRequest{
		Source: `printf changed > data.txt`,
		Files:  map[string][]byte{"data.txt": []byte("original")},
	}
	simulator := NewBuilder().Build()
	if err := simulator.Simulate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got := string(request.Files["data.txt"]); got != "original" {
		t.Fatalf("request file changed to %q", got)
	}
}

func TestSimulatorRejectsAmbiguousInitialFilePaths(t *testing.T) {
	simulator := NewBuilder().Build()
	err := simulator.Simulate(context.Background(), &SimulationRequest{
		Files: map[string][]byte{
			"config.sh":   []byte("first"),
			"./config.sh": []byte("second"),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "same virtual path") {
		t.Fatalf("Simulate() error = %v", err)
	}
}
