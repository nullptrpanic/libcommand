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
	output, err := executeSeq(ctx, invocation, command.MaxMemoryBytes())
	if err != nil {
		return nil, err
	}
	return command.Result(output), nil
}

func executeSeq(ctx context.Context, invocation *runtime.Invocation, maximum int) (*runtime.CommandOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(invocation.Args) < 1 || len(invocation.Args) > 3 {
		return commandOutput(nil, []byte("seq: expected 1 to 3 integer arguments\n"), 1), nil
	}

	values := make([]int64, len(invocation.Args))
	for index, argument := range invocation.Args {
		if argument.Kind != runtime.ArgumentString {
			return unresolvedCommandOutput(), nil
		}
		value, err := strconv.ParseInt(argument.Value, 10, 64)
		if err != nil {
			return commandOutput(nil, []byte(fmt.Sprintf("seq: invalid integer %q\n", argument.Value)), 1), nil
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
		return commandOutput(nil, []byte("seq: step must not be zero\n"), 1), nil
	}
	if step > 0 && first > last || step < 0 && first < last {
		return commandOutput(nil, nil, 0), nil
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
	return commandOutput(stdout, nil, 0), nil
}
