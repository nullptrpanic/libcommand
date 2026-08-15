package builtin

import (
	"strconv"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func parseNonNegative(value string) (int, bool) {
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil && parsed >= 0
}

func addMaterializedString(total int, value string, maximum int) (int, error) {
	var ok bool
	total, ok = materialize.Add(total, materialize.EntryBytes, maximum)
	if ok {
		total, ok = materialize.Add(total, len(value), maximum)
	}
	if !ok {
		return 0, materialize.LimitError(maximum)
	}
	return total, nil
}

func consumeInputRecord(shell *runtime.CommandContext, delimiter byte) (string, bool) {
	return consumeInput(shell, delimiter, 0, false)
}

func consumeInput(shell *runtime.CommandContext, delimiter byte, maximum int, exact bool) (string, bool) {
	input, terminated, _ := shell.ConsumeInput(delimiter, maximum, exact, false)
	return string(input), terminated
}

func consumeReadInput(shell *runtime.CommandContext, delimiter byte, maximum int, exact, raw bool) (string, bool) {
	input, terminated, _ := shell.ConsumeInput(delimiter, maximum, exact, !raw && maximum == 0)
	return string(input), terminated
}
