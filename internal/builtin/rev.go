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
	output, consumed, err := executeRev(ctx, invocation, command.MaxMemoryBytes())
	if err != nil {
		return nil, err
	}
	if consumed {
		command.SetInput(nil, false)
	}
	return command.Result(output), nil
}

func executeRev(ctx context.Context, invocation *runtime.Invocation, maximum int) (*runtime.CommandOutput, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if len(invocation.Args) != 0 {
		return commandOutput(nil, []byte("rev: file operands are not supported\n"), 1), false, nil
	}
	if invocation.Unresolved != nil && invocation.Unresolved.Stdin {
		return unresolvedCommandOutput(), false, nil
	}
	if _, ok := materialize.Add(0, len(invocation.Stdin), maximum); !ok {
		return nil, false, materialize.LimitError(maximum)
	}

	stdout := make([]byte, 0, len(invocation.Stdin))
	for remaining := invocation.Stdin; len(remaining) != 0; {
		lineEnd := bytes.IndexByte(remaining, '\n')
		if lineEnd < 0 {
			var err error
			stdout, err = appendReversedRunes(ctx, stdout, remaining)
			if err != nil {
				return nil, false, err
			}
			break
		}
		var err error
		stdout, err = appendReversedRunes(ctx, stdout, remaining[:lineEnd])
		if err != nil {
			return nil, false, err
		}
		stdout = append(stdout, '\n')
		remaining = remaining[lineEnd+1:]
	}
	return commandOutput(stdout, nil, 0), true, nil
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
