package builtin

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func init() {
	registerCommand("read", executeRead)
}

func executeRead(ctx context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return shell.ResultUnknown(&runtime.CommandResult{ExitCode: 1}, false, true, false), nil
	}
	originalInput, originalUnresolved := shell.Input()
	restoreInput := func() {
		shell.SetInput(originalInput, originalUnresolved)
	}
	raw := false
	arrayName := ""
	delimiter := byte('\n')
	maximum := 0
	exact := false
	index := 0
	for index < len(args) && strings.HasPrefix(args[index], "-") && args[index] != "-" {
		option := args[index]
		if option == "--" {
			index++
			break
		}
		switch option {
		case "-r":
			raw = true
		case "-s", "-e":
		case "-a":
			index++
			if index >= len(args) {
				return readUsageError(), nil
			}
			arrayName = args[index]
		case "-d":
			index++
			if index >= len(args) {
				return readUsageError(), nil
			}
			if args[index] == "" {
				delimiter = 0
			} else {
				delimiter = args[index][0]
			}
		case "-n", "-N":
			index++
			if index >= len(args) {
				return readUsageError(), nil
			}
			var valid bool
			maximum, valid = parseNonNegative(args[index])
			if !valid {
				return readUsageError(), nil
			}
			exact = option == "-N"
		case "-u":
			index++
			if index >= len(args) {
				return readUsageError(), nil
			}
			fileDescriptor, valid := parseNonNegative(args[index])
			if !valid {
				return readUsageError(), nil
			}
			if fileDescriptor != 0 {
				return &runtime.CommandResult{Stderr: []byte("read: only file descriptor 0 is supported\n"), ExitCode: 2}, nil
			}
		case "-t":
			return &runtime.CommandResult{Stderr: []byte("read: -t is not supported\n"), ExitCode: 2}, nil
		case "-p", "-i":
			index++
			if index >= len(args) {
				return readUsageError(), nil
			}
		default:
			return readUsageError(), nil
		}
		index++
	}
	var names []string
	if arrayName != "" {
		if !syntax.ValidName(arrayName) {
			return &runtime.CommandResult{Stderr: []byte(fmt.Sprintf("read: `%s': not a valid identifier\n", arrayName)), ExitCode: 1}, nil
		}
		if shell.Variable(arrayName).ReadOnly {
			return &runtime.CommandResult{Stderr: []byte(fmt.Sprintf("read: %s: readonly variable\n", arrayName)), ExitCode: 1}, nil
		}
	} else {
		names = args[index:]
		if len(names) == 0 {
			names = []string{"REPLY"}
		}
		for _, name := range names {
			if !syntax.ValidName(name) {
				return &runtime.CommandResult{Stderr: []byte(fmt.Sprintf("read: `%s': not a valid identifier\n", name)), ExitCode: 1}, nil
			}
			if shell.Variable(name).ReadOnly {
				return &runtime.CommandResult{Stderr: []byte(fmt.Sprintf("read: %s: readonly variable\n", name)), ExitCode: 1}, nil
			}
		}
	}
	_, inputUnknown := shell.Input()
	if inputUnknown {
		if arrayName != "" {
			if err := shell.AssignVariable(arrayName, &expand.Variable{Set: true, Kind: expand.Indexed}, true); err != nil {
				return nil, err
			}
		} else {
			for _, name := range names {
				if err := shell.AssignVariable(name, &expand.Variable{Set: true, Kind: expand.String}, true); err != nil {
					return nil, err
				}
			}
		}
		shell.SetInput(nil, false)
		return shell.ResultUnknown(&runtime.CommandResult{}, false, false, true), nil
	}
	line, terminated := consumeReadInput(shell, delimiter, maximum, exact, raw)
	if shell.VariableUnknown("IFS") {
		if arrayName != "" {
			if err := shell.AssignVariable(arrayName, &expand.Variable{Set: true, Kind: expand.Indexed, List: []string{}}, true); err != nil {
				restoreInput()
				return nil, err
			}
		} else {
			for _, name := range names {
				if err := shell.AssignVariable(name, &expand.Variable{Set: true, Kind: expand.String}, true); err != nil {
					restoreInput()
					return nil, err
				}
			}
		}
		if !terminated {
			return &runtime.CommandResult{ExitCode: 1}, nil
		}
		return &runtime.CommandResult{}, nil
	}
	var escaped []bool
	if !raw {
		line, escaped = decodeReadBackslashes(line)
	}
	fields, splitEnd, err := splitReadFields(ctx, shell, line, escaped)
	if err != nil {
		restoreInput()
		return nil, err
	}
	if arrayName != "" {
		values := make([]string, len(fields))
		for index, field := range fields {
			values[index] = field.value
		}
		if err := shell.AssignVariable(arrayName, &expand.Variable{Set: true, Kind: expand.Indexed, List: values}, false); err != nil {
			restoreInput()
			return nil, err
		}
	} else if err := assignReadFields(shell, names, fields, line, splitEnd); err != nil {
		restoreInput()
		return nil, err
	}
	if !terminated {
		return &runtime.CommandResult{ExitCode: 1}, nil
	}
	return &runtime.CommandResult{}, nil
}

func readUsageError() *runtime.CommandResult {
	return &runtime.CommandResult{Stderr: []byte("read: invalid option\n"), ExitCode: 2}
}

func decodeReadBackslashes(value string) (string, []bool) {
	result := make([]byte, 0, len(value))
	escaped := make([]bool, 0, len(value))
	for index := 0; index < len(value); index++ {
		if value[index] == '\\' && index+1 < len(value) {
			index++
			result = append(result, value[index])
			escaped = append(escaped, true)
			continue
		}
		result = append(result, value[index])
		escaped = append(escaped, false)
	}
	return string(result), escaped
}

type readField struct {
	value string
	start int
}

func splitReadFields(ctx context.Context, shell *runtime.CommandContext, value string, escaped []bool) ([]*readField, int, error) {
	ifs := shell.Variable("IFS")
	separators := " \t\n"
	if ifs.IsSet() {
		separators = ifs.String()
	}
	if separators == "" {
		if _, err := addMaterializedString(0, value, shell.MaxMemoryBytes()); err != nil {
			return nil, 0, err
		}
		return []*readField{{value: value}}, len(value), nil
	}
	separatorSet, err := newReadSeparatorSet(ctx, separators, shell.MaxMemoryBytes())
	if err != nil {
		return nil, 0, err
	}
	classifyAt := func(index int) (int, bool, bool) {
		_, size := utf8.DecodeRuneInString(value[index:])
		_, separator := separatorSet[value[index:index+size]]
		separator = separator && (len(escaped) <= index || !escaped[index])
		whitespace := separator && size == 1 && (value[index] == ' ' || value[index] == '\t' || value[index] == '\n')
		return size, separator, whitespace
	}
	classifyBefore := func(end int) (int, bool) {
		_, size := utf8.DecodeLastRuneInString(value[:end])
		_, separator := separatorSet[value[end-size:end]]
		separator = separator && (len(escaped) <= end-size || !escaped[end-size])
		return size, separator
	}
	skipWhitespace := func(index int) int {
		for index < len(value) {
			size, _, whitespace := classifyAt(index)
			if !whitespace {
				break
			}
			index += size
		}
		return index
	}

	end := len(value)
	for end > 0 {
		size, separator := classifyBefore(end)
		if !separator {
			break
		}
		end -= size
	}
	index := skipWhitespace(0)
	fields := make([]*readField, 0)
	materializedBytes := 0
	for index < len(value) {
		if len(fields)%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
		}
		start := index
		for index < len(value) {
			size, separator, _ := classifyAt(index)
			if separator {
				break
			}
			index += size
		}
		field := value[start:index]
		materializedBytes, err = addMaterializedString(materializedBytes, field, shell.MaxMemoryBytes())
		if err != nil {
			return nil, 0, err
		}
		fields = append(fields, &readField{value: field, start: start})
		if index == len(value) {
			break
		}
		size, _, whitespace := classifyAt(index)
		if whitespace {
			index = skipWhitespace(index)
			if index < len(value) {
				size, separator, whitespace := classifyAt(index)
				if separator && !whitespace {
					index = skipWhitespace(index + size)
				}
			}
		} else {
			index = skipWhitespace(index + size)
		}
	}
	return fields, end, nil
}

func newReadSeparatorSet(ctx context.Context, value string, maximum int) (map[string]struct{}, error) {
	set := make(map[string]struct{})
	materializedBytes := 0
	for index, count := 0, 0; index < len(value); count++ {
		if count%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		_, size := utf8.DecodeRuneInString(value[index:])
		separator := value[index : index+size]
		if _, exists := set[separator]; !exists {
			next, ok := materialize.Add(materializedBytes, materialize.EntryBytes, maximum)
			if ok {
				next, ok = materialize.Add(next, len(separator), maximum)
			}
			if !ok {
				return nil, materialize.LimitError(maximum)
			}
			materializedBytes = next
			set[separator] = struct{}{}
		}
		index += size
	}
	return set, nil
}

func assignReadFields(shell *runtime.CommandContext, names []string, fields []*readField, original string, splitEnd int) error {
	values := make([]*expand.Variable, len(names))
	for index := range names {
		value := ""
		if index < len(fields) {
			if index == len(names)-1 {
				if fields[index].start < splitEnd {
					value = original[fields[index].start:splitEnd]
				}
			} else {
				value = fields[index].value
			}
		}
		values[index] = &expand.Variable{Set: true, Kind: expand.String, Str: value}
	}
	for index, name := range names {
		if err := shell.AssignVariable(name, values[index], false); err != nil {
			return err
		}
	}
	return nil
}
