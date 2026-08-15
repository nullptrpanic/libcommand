package builtin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func TestRevReversesEachInputLine(t *testing.T) {
	result, err := runRev(context.Background(), []byte("abc\n世界\nlast"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || string(result.Stdout) != "cba\n界世\ntsal" || len(result.Stderr) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRevRejectsFileOperands(t *testing.T) {
	result, err := runRev(context.Background(), nil, []string{"input.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 1 || len(result.Stdout) != 0 || string(result.Stderr) != "rev: file operands are not supported\n" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRevDeclinesUnresolvedStdin(t *testing.T) {
	result, err := executeRev(context.Background(), &runtime.Invocation{
		Name:       "rev",
		Unresolved: &runtime.InvocationUnresolved{Stdin: true},
	}, 2<<20)
	if err != nil || result == nil || !result.Unresolved {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestRevHonorsMaterializationLimit(t *testing.T) {
	invocation := &runtime.Invocation{Name: "rev", Stdin: []byte("abc\n")}
	result, err := executeRev(context.Background(), invocation, 4)
	if err != nil || string(result.Stdout) != "cba\n" {
		t.Fatalf("exact limit result = %#v, error = %v", result, err)
	}
	result, err = executeRev(context.Background(), invocation, 3)
	if result != nil || err == nil || err.Error() != "maximum materialized byte count 3 reached" {
		t.Fatalf("over limit result = %#v, error = %v", result, err)
	}
}

func TestRevPreservesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := runRev(ctx, []byte("input"), nil)
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestRevChecksCancellationWhileProcessing(t *testing.T) {
	ctx := &cancelAfterFirstCheckContext{done: make(chan struct{})}
	result, err := runRev(ctx, []byte("first\nsecond\n"), nil)
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

type cancelAfterFirstCheckContext struct {
	done   chan struct{}
	checks int
}

func (*cancelAfterFirstCheckContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *cancelAfterFirstCheckContext) Done() <-chan struct{} {
	return c.done
}

func (c *cancelAfterFirstCheckContext) Err() error {
	c.checks++
	if c.checks == 1 {
		return nil
	}
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return context.Canceled
}

func (*cancelAfterFirstCheckContext) Value(any) any {
	return nil
}

func runRev(ctx context.Context, stdin []byte, args []string) (*runtime.CommandResult, error) {
	arguments := make([]*runtime.Argument, len(args))
	for index, argument := range args {
		arguments[index] = &runtime.Argument{Kind: runtime.ArgumentString, Value: argument}
	}
	return executeRev(ctx, &runtime.Invocation{Name: "rev", Args: arguments, Stdin: stdin}, 2<<20)
}
