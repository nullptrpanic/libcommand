package builtin

import (
	"context"
	"errors"
	"testing"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func TestSeqGeneratesIntegerRanges(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		stdout string
	}{
		{name: "last", args: []string{"3"}, stdout: "1\n2\n3\n"},
		{name: "first last", args: []string{"-1", "1"}, stdout: "-1\n0\n1\n"},
		{name: "descending", args: []string{"5", "-2", "1"}, stdout: "5\n3\n1\n"},
		{name: "ascending direction mismatch", args: []string{"5", "1"}, stdout: ""},
		{name: "descending direction mismatch", args: []string{"1", "-1", "5"}, stdout: ""},
		{
			name:   "positive boundary",
			args:   []string{"9223372036854775806", "9223372036854775807"},
			stdout: "9223372036854775806\n9223372036854775807\n",
		},
		{
			name:   "negative boundary",
			args:   []string{"-9223372036854775807", "-1", "-9223372036854775808"},
			stdout: "-9223372036854775807\n-9223372036854775808\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := runSeq(context.Background(), test.args)
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode.Value != 0 || string(result.Stdout.Value) != test.stdout || len(result.Stderr.Value) != 0 {
				t.Fatalf("result = %#v, want stdout %q", result, test.stdout)
			}
		})
	}
}

func TestSeqRejectsInvalidArguments(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		stderr string
	}{
		{name: "missing", stderr: "seq: expected 1 to 3 integer arguments\n"},
		{name: "too many", args: []string{"1", "2", "3", "4"}, stderr: "seq: expected 1 to 3 integer arguments\n"},
		{name: "not integer", args: []string{"two"}, stderr: "seq: invalid integer \"two\"\n"},
		{name: "zero step", args: []string{"1", "0", "2"}, stderr: "seq: step must not be zero\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := runSeq(context.Background(), test.args)
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode.Value != 1 || len(result.Stdout.Value) != 0 || string(result.Stderr.Value) != test.stderr {
				t.Fatalf("result = %#v, want stderr %q", result, test.stderr)
			}
		})
	}
}

func TestSeqPreservesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := runSeq(ctx, []string{"3"})
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestSeqHonorsMaterializationLimit(t *testing.T) {
	invocation := &runtime.Invocation{
		Name: "seq",
		Args: []*runtime.Argument{{Kind: runtime.ArgumentString, Value: "3"}},
	}
	result, err := executeSeq(context.Background(), invocation, 6)
	if err != nil || string(result.Stdout.Value) != "1\n2\n3\n" {
		t.Fatalf("exact limit result = %#v, error = %v", result, err)
	}
	result, err = executeSeq(context.Background(), invocation, 5)
	if result != nil || err == nil || err.Error() != "maximum materialized byte count 5 reached" {
		t.Fatalf("over limit result = %#v, error = %v", result, err)
	}
}

func runSeq(ctx context.Context, args []string) (*runtime.CommandOutput, error) {
	arguments := make([]*runtime.Argument, len(args))
	for index, argument := range args {
		arguments[index] = &runtime.Argument{Kind: runtime.ArgumentString, Value: argument}
	}
	return executeSeq(ctx, &runtime.Invocation{Name: "seq", Args: arguments}, 2<<20)
}
