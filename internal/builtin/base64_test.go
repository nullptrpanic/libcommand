package builtin

import (
	"context"
	"errors"
	"testing"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func TestBase64EncodesStdin(t *testing.T) {
	tests := []struct {
		name   string
		stdin  []byte
		stdout string
	}{
		{name: "empty"},
		{name: "text", stdin: []byte("hello"), stdout: "aGVsbG8=\n"},
		{name: "binary", stdin: []byte{0, 1, 2, 255}, stdout: "AAEC/w==\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := runBase64(context.Background(), nil, test.stdin)
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != 0 || string(result.Stdout) != test.stdout || len(result.Stderr) != 0 {
				t.Fatalf("result = %#v, want stdout %q", result, test.stdout)
			}
		})
	}
}

func TestBase64DecodesStdin(t *testing.T) {
	for _, option := range []string{"-d", "--decode", "-D"} {
		t.Run(option, func(t *testing.T) {
			result, err := runBase64(context.Background(), []string{option}, []byte("aGVs\nbG8="))
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != 0 || string(result.Stdout) != "hello" || len(result.Stderr) != 0 {
				t.Fatalf("result = %#v, want decoded output", result)
			}
		})
	}
}

func TestBase64RejectsInvalidInputAndArguments(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		stdin  []byte
		stderr string
	}{
		{name: "invalid input", args: []string{"-d"}, stdin: []byte("%%%"), stderr: "base64: invalid input\n"},
		{name: "unsupported option", args: []string{"-w"}, stderr: "base64: unsupported option \"-w\"\n"},
		{name: "file operand", args: []string{"input.txt"}, stderr: "base64: file operands are not supported\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := runBase64(context.Background(), test.args, test.stdin)
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != 1 || len(result.Stdout) != 0 || string(result.Stderr) != test.stderr {
				t.Fatalf("result = %#v, want stderr %q", result, test.stderr)
			}
		})
	}
}

func TestBase64PreservesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := runBase64(ctx, nil, []byte("hello"))
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestBase64ReportsUnresolvedArgumentsExplicitly(t *testing.T) {
	result, err := executeBase64(context.Background(), &runtime.Invocation{
		Name: "base64",
		Args: []*runtime.Argument{{Kind: runtime.ArgumentUnresolved}},
	}, 2<<20)
	if err != nil || result == nil || !result.Unresolved {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestBase64HonorsMaterializationLimit(t *testing.T) {
	encode := &runtime.Invocation{Name: "base64", Stdin: []byte("hello")}
	result, err := executeBase64(context.Background(), encode, 9)
	if err != nil || string(result.Stdout) != "aGVsbG8=\n" {
		t.Fatalf("exact encode result = %#v, error = %v", result, err)
	}
	result, err = executeBase64(context.Background(), encode, 8)
	if result != nil || err == nil || err.Error() != "maximum materialized byte count 8 reached" {
		t.Fatalf("over encode result = %#v, error = %v", result, err)
	}

	decode := &runtime.Invocation{
		Name:  "base64",
		Args:  []*runtime.Argument{{Kind: runtime.ArgumentString, Value: "-d"}},
		Stdin: []byte("aGVs\nbG8="),
	}
	result, err = executeBase64(context.Background(), decode, 5)
	if err != nil || string(result.Stdout) != "hello" {
		t.Fatalf("exact decode result = %#v, error = %v", result, err)
	}
	result, err = executeBase64(context.Background(), decode, 4)
	if result != nil || err == nil || err.Error() != "maximum materialized byte count 4 reached" {
		t.Fatalf("over decode result = %#v, error = %v", result, err)
	}
}

func runBase64(ctx context.Context, args []string, stdin []byte) (*runtime.CommandResult, error) {
	arguments := make([]*runtime.Argument, len(args))
	for index, argument := range args {
		arguments[index] = &runtime.Argument{Kind: runtime.ArgumentString, Value: argument}
	}
	return executeBase64(ctx, &runtime.Invocation{Name: "base64", Args: arguments, Stdin: stdin}, 2<<20)
}
