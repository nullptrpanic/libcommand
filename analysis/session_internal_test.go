package analysis

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nullptrpanic/libcommand"
)

func TestInspectWithoutSession(t *testing.T) {
	if err := Inspect(context.Background(), nil, &libcommand.Invocation{}, nil); err != nil {
		t.Fatalf("nil detector = %v", err)
	}
	want := errors.New("detected")
	called := false
	detector := func(_ context.Context, _ *libcommand.CommandContext, _ *libcommand.Invocation) (*libcommand.CommandResult, error) {
		called = true
		return nil, want
	}
	if err := Inspect(context.Background(), nil, &libcommand.Invocation{}, detector); !errors.Is(err, want) || !called {
		t.Fatalf("detector result = %v, called %v", err, called)
	}
}

func TestSessionFactAndObservationHelpers(t *testing.T) {
	session := NewSession()
	state := new(libcommand.State)
	first := session.facts(state)
	first.stream = streamFact{stage: streamFIFO, fifo: "/tmp/f"}
	if session.facts(state) != first {
		t.Fatal("facts were not cached for a path")
	}
	clone := clonePathFacts(first)
	if clone == first || clone.stream != first.stream || clone.fifos != first.fifos {
		t.Fatalf("cloned facts = %#v", clone)
	}
	if currentObservation(context.Background()) != nil {
		t.Fatal("plain context unexpectedly has an observation")
	}
	observation := &observation{facts: &pathFacts{}}
	ctx := context.WithValue(context.Background(), observationContextKey{}, observation)
	if currentObservation(ctx) != observation {
		t.Fatal("observation was not recovered from context")
	}

	observeMkfifo(context.Background(), nil, &libcommand.Invocation{})
	observeCat(context.Background(), nil, &libcommand.Invocation{})
	observeInteractiveShell(context.Background(), nil, &libcommand.Invocation{}, true)
	if observeNetcat(context.Background(), nil, &libcommand.Invocation{}) {
		t.Fatal("netcat without an observation completed a stream chain")
	}
}

func TestFIFOFactHistoryAndBounds(t *testing.T) {
	facts := &pathFacts{}
	setFIFO(facts, "/tmp/f", true)
	setFIFO(facts, "/tmp/f", true)
	if !fifoTracked(facts.fifos, "/tmp/f") || fifoCount(facts.fifos) != 1 {
		t.Fatalf("FIFO facts = %#v", facts.fifos)
	}
	facts.stream = streamFact{stage: streamFIFO, fifo: "/tmp/f"}
	setFIFO(facts, "/tmp/f", false)
	if fifoTracked(facts.fifos, "/tmp/f") || facts.stream.stage != streamNone {
		t.Fatalf("removed FIFO facts = %#v, stream %#v", facts.fifos, facts.stream)
	}
	setFIFO(facts, strings.Repeat("x", maximumTrackedPathSize+1), true)
	if fifoCount(facts.fifos) != 2 {
		t.Fatalf("oversized path changed FIFO facts: %#v", facts.fifos)
	}

	bounded := &pathFacts{}
	for index := 0; index < maximumTrackedFIFOs; index++ {
		setFIFO(bounded, string(rune(index+1)), true)
	}
	setFIFO(bounded, string(rune(1)), false)
	if bounded.fifos != nil {
		t.Fatal("FIFO history above the bound was retained")
	}
	setFIFO(bounded, "fresh", true)
	if !fifoTracked(bounded.fifos, "fresh") || fifoCount(bounded.fifos) != 1 {
		t.Fatalf("fresh FIFO after reset = %#v", bounded.fifos)
	}
}

func TestSessionArgumentParsers(t *testing.T) {
	argument := func(value string) *libcommand.Argument {
		return &libcommand.Argument{Kind: libcommand.ArgumentString, Value: value}
	}
	for _, test := range []*struct {
		name string
		args []*libcommand.Argument
		want []string
	}{
		{name: "plain", args: []*libcommand.Argument{argument("/tmp/a")}, want: []string{"/tmp/a"}},
		{name: "options", args: []*libcommand.Argument{argument("-m"), argument("600"), argument("--mode=700"), argument("--"), argument("-name")}, want: []string{"-name"}},
		{name: "missing mode", args: []*libcommand.Argument{argument("-m")}},
		{name: "unsupported option", args: []*libcommand.Argument{argument("-Z"), argument("/tmp/a")}},
		{name: "unresolved", args: []*libcommand.Argument{{Kind: libcommand.ArgumentUnresolved}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := mkfifoOperands(test.args)
			if strings.Join(got, "\x00") != strings.Join(test.want, "\x00") {
				t.Fatalf("mkfifoOperands() = %#v, want %#v", got, test.want)
			}
		})
	}

	if got, ok := singleCatOperand([]*libcommand.Argument{argument("--"), argument("-file")}); !ok || got != "-file" {
		t.Fatalf("cat operand = %q, %v", got, ok)
	}
	for _, args := range [][]*libcommand.Argument{
		{argument("-n"), argument("file")},
		{argument("-")},
		{argument("one"), argument("two")},
		{{Kind: libcommand.ArgumentUnresolved}},
	} {
		if got, ok := singleCatOperand(args); ok || got != "" {
			t.Fatalf("invalid cat operands %#v = %q, %v", args, got, ok)
		}
	}
}

func TestPipelineInputAndOutputRecognition(t *testing.T) {
	invocation := &libcommand.Invocation{Unresolved: &libcommand.InvocationUnresolved{Stdin: true}}
	if !unresolvedPipelineInput(invocation, nil) {
		t.Fatal("unresolved pipeline input was not recognized")
	}
	invocation.Stdin = []byte("representative")
	if unresolvedPipelineInput(invocation, nil) {
		t.Fatal("non-empty representative input was treated as a pipeline")
	}
	invocation.Stdin = nil
	if unresolvedPipelineInput(invocation, []*libcommand.Redirect{{FD: 0, Operator: "<", Target: "/input"}}) {
		t.Fatal("redirected stdin was treated as pipeline input")
	}

	redirects := []*libcommand.Redirect{
		{FD: 2, Operator: ">", Target: "/tmp/f"},
		{FD: 1, Operator: ">", Target: "/tmp/other"},
		{FD: 1, Operator: ">", Target: "/tmp/f", Unresolved: true},
		{FD: 1, Operator: "<", Target: "/tmp/f"},
		{FD: 1, Operator: ">>", Target: "/tmp/dir/../f"},
	}
	if !redirectsStdoutTo(redirects, "/tmp/f") || redirectsStdoutTo(redirects, "/tmp/missing") {
		t.Fatalf("redirect matching failed: %#v", redirects)
	}
}

func TestRecursiveRootRemovalHelper(t *testing.T) {
	if !recursiveRootRemoval([]string{"-rf", "/"}, "/work", false) {
		t.Fatal("recursive root removal was not recognized")
	}
	if recursiveRootRemoval([]string{"-f", "/"}, "/work", false) {
		t.Fatal("non-recursive root removal was recognized")
	}
}

func TestInterpreterAndFindArgumentHelpers(t *testing.T) {
	if value, ok := shortOptionPayload([]string{"-e", "/bin/sh"}, "-e"); !ok || value != "/bin/sh" {
		t.Fatalf("separate short option = %q, %v", value, ok)
	}
	if value, ok := shortOptionPayload([]string{"-e/bin/bash"}, "-e"); !ok || value != "/bin/bash" {
		t.Fatalf("attached short option = %q, %v", value, ok)
	}
	for _, arguments := range [][]string{{"-e"}, {"--", "-e", "/bin/sh"}, {"other"}} {
		if value, ok := shortOptionPayload(arguments, "-e"); ok || value != "" {
			t.Fatalf("missing short option in %#v = %q, %v", arguments, value, ok)
		}
	}
	if !containsShellExecutable("exec /BIN/BASH -i") || containsShellExecutable("exec /usr/bin/uptime") {
		t.Fatal("shell executable classification mismatch")
	}

	paths, _ := findSearchPaths([]string{"-D", "debug", "-H", "-O2", "/one", "/two", "-name", "value"})
	if strings.Join(paths, ",") != "/one,/two" {
		t.Fatalf("find search paths = %#v", paths)
	}
	if paths, _ := findSearchPaths([]string{"--", "relative", "!", "-name", "value"}); strings.Join(paths, ",") != "relative" {
		t.Fatalf("find expression paths = %#v", paths)
	}
}
