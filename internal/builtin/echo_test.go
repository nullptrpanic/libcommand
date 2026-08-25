package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func TestEchoEscapesMatchBashBytes(t *testing.T) {
	tests := map[string]string{
		`\0`:   string([]byte{0}),
		`\08`:  string([]byte{0, '8'}),
		`\x`:   `\x`,
		`\x4g`: string([]byte{4, 'g'}),
	}
	for input, want := range tests {
		result, err := executeEcho(context.Background(), &runtime.Invocation{Args: []*runtime.Argument{
			{Kind: runtime.ArgumentString, Value: "-en"},
			{Kind: runtime.ArgumentString, Value: input},
		}}, 1024)
		if err != nil || string(result.Stdout.Value) != want {
			t.Fatalf("echo %q = %q, %v; want %q", input, result.Stdout.Value, err, want)
		}
	}
}

func TestEchoHonorsMaterializationLimit(t *testing.T) {
	result, err := executeEcho(context.Background(), &runtime.Invocation{Args: []*runtime.Argument{
		{Kind: runtime.ArgumentString, Value: strings.Repeat("x", 9)},
	}}, 8)
	if result != nil || err == nil {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}
