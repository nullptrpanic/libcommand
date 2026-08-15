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
	input, unresolved := shell.Input()
	if maximum > 0 && exact {
		count := min(maximum, len(input))
		line := string(input[:count])
		shell.SetInput(input[count:len(input):len(input)], unresolved)
		return line, count == maximum
	}
	for index, value := range input {
		if value == delimiter {
			line := string(input[:index])
			shell.SetInput(input[index+1:len(input):len(input)], unresolved)
			return line, true
		}
		if maximum > 0 && index+1 == maximum {
			line := string(input[:maximum])
			shell.SetInput(input[maximum:len(input):len(input)], unresolved)
			return line, true
		}
	}
	line := string(input)
	shell.SetInput(nil, unresolved)
	return line, false
}

func consumeReadInput(shell *runtime.CommandContext, delimiter byte, maximum int, exact, raw bool) (string, bool) {
	if raw || maximum > 0 {
		return consumeInput(shell, delimiter, maximum, exact)
	}
	input, unresolved := shell.Input()
	var output []byte
	segmentStart := 0
	for index, value := range input {
		if value != delimiter {
			continue
		}
		backslashes := 0
		for position := index - 1; position >= segmentStart && input[position] == '\\'; position-- {
			backslashes++
		}
		if backslashes%2 != 0 {
			if delimiter == '\n' {
				output = append(output, input[segmentStart:index-1]...)
				segmentStart = index + 1
			}
			continue
		}
		if output == nil {
			line := string(input[:index])
			shell.SetInput(input[index+1:len(input):len(input)], unresolved)
			return line, true
		}
		output = append(output, input[segmentStart:index]...)
		shell.SetInput(input[index+1:len(input):len(input)], unresolved)
		return string(output), true
	}
	output = append(output, input[segmentStart:]...)
	shell.SetInput(nil, unresolved)
	return string(output), false
}
