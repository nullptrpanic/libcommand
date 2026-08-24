package builtin

import (
	"bytes"
	"context"
	"unicode/utf8"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	register("rev", executeRevCommand)
}

func executeRevCommand(ctx context.Context, command *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	return executeRev(ctx, invocation, command.MaxMemoryBytes())
}

func executeRev(ctx context.Context, invocation *runtime.Invocation, maximum int) (*runtime.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(invocation.Args) != 0 {
		return revFailure("rev: file operands are not supported\n"), nil
	}
	if invocation.Unresolved != nil && invocation.Unresolved.Stdin {
		return runtime.NewUnresolvedResult(), nil
	}
	if _, ok := materialize.Add(0, len(invocation.Stdin), maximum); !ok {
		return nil, materialize.LimitError(maximum)
	}

	stdout := make([]byte, 0, len(invocation.Stdin))
	for remaining := invocation.Stdin; len(remaining) != 0; {
		lineEnd := bytes.IndexByte(remaining, '\n')
		if lineEnd < 0 {
			var err error
			stdout, err = appendReversedRunes(ctx, stdout, remaining)
			if err != nil {
				return nil, err
			}
			break
		}
		var err error
		stdout, err = appendReversedRunes(ctx, stdout, remaining[:lineEnd])
		if err != nil {
			return nil, err
		}
		stdout = append(stdout, '\n')
		remaining = remaining[lineEnd+1:]
	}
	return &runtime.CommandResult{Stdout: stdout}, nil
}

func appendReversedRunes(ctx context.Context, destination, source []byte) ([]byte, error) {
	iterations := 0
	for len(source) != 0 {
		if iterations%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		_, size := utf8.DecodeLastRune(source)
		destination = append(destination, source[len(source)-size:]...)
		source = source[:len(source)-size]
		iterations++
	}
	return destination, nil
}

func revFailure(message string) *runtime.CommandResult {
	return &runtime.CommandResult{Stderr: []byte(message), ExitCode: 1}
}
