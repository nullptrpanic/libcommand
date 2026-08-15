package builtin

import (
	"context"
	"fmt"
	"strconv"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	register("seq", executeSeqCommand)
}

func executeSeqCommand(ctx context.Context, command *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	return executeSeq(ctx, invocation, command.MaxMemoryBytes())
}

func executeSeq(ctx context.Context, invocation *runtime.Invocation, maximum int) (*runtime.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(invocation.Args) < 1 || len(invocation.Args) > 3 {
		return seqFailure("seq: expected 1 to 3 integer arguments\n"), nil
	}

	values := make([]int64, len(invocation.Args))
	for index, argument := range invocation.Args {
		if argument.Kind != runtime.ArgumentString {
			return &runtime.CommandResult{Unresolved: true}, nil
		}
		value, err := strconv.ParseInt(argument.Value, 10, 64)
		if err != nil {
			return seqFailure(fmt.Sprintf("seq: invalid integer %q\n", argument.Value)), nil
		}
		values[index] = value
	}

	first, step, last := int64(1), int64(1), values[0]
	switch len(values) {
	case 2:
		first, last = values[0], values[1]
	case 3:
		first, step, last = values[0], values[1], values[2]
	}
	if step == 0 {
		return seqFailure("seq: step must not be zero\n"), nil
	}
	if step > 0 && first > last || step < 0 && first < last {
		return &runtime.CommandResult{}, nil
	}

	var stdout []byte
	for current := first; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if step > 0 && current > last || step < 0 && current < last {
			break
		}
		number := strconv.AppendInt(nil, current, 10)
		nextLength, ok := materialize.Add(len(stdout), len(number), maximum)
		if ok {
			_, ok = materialize.Add(nextLength, 1, maximum)
		}
		if !ok {
			return nil, materialize.LimitError(maximum)
		}
		stdout = append(stdout, number...)
		stdout = append(stdout, '\n')
		next := current + step
		if step > 0 && next < current || step < 0 && next > current {
			break
		}
		current = next
	}
	return &runtime.CommandResult{Stdout: stdout}, nil
}

func seqFailure(message string) *runtime.CommandResult {
	return &runtime.CommandResult{Stderr: []byte(message), ExitCode: 1}
}
