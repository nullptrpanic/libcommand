package builtin

import (
	"context"
	"strconv"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerCommand("echo", executeEchoCommand)
}

func executeEchoCommand(ctx context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	result, err := executeEcho(ctx, invocation, shell.MaxMemoryBytes())
	if err != nil {
		return nil, err
	}
	if result.Unresolved {
		return shell.ResultUnknown(&runtime.CommandResult{}, true, false, false), nil
	}
	return result, nil
}

func executeEcho(ctx context.Context, invocation *runtime.Invocation, maximum int) (*runtime.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args := make([]string, len(invocation.Args))
	total := 0
	for index, argument := range invocation.Args {
		if argument.Kind != runtime.ArgumentString {
			return &runtime.CommandResult{Unresolved: true}, nil
		}
		var ok bool
		total, ok = materialize.Add(total, len(argument.Value), maximum)
		if !ok {
			return nil, materialize.LimitError(maximum)
		}
		args[index] = argument.Value
	}
	if len(args) > 1 {
		var ok bool
		_, ok = materialize.Add(total, len(args)-1, maximum)
		if !ok {
			return nil, materialize.LimitError(maximum)
		}
	}

	newline, escapes, args := parseEchoOptions(args)
	output := strings.Join(args, " ")
	if escapes {
		var stop bool
		output, stop = decodeEchoEscapes(output)
		if stop {
			newline = false
		}
	}
	if newline {
		if _, ok := materialize.Add(len(output), 1, maximum); !ok {
			return nil, materialize.LimitError(maximum)
		}
		output += "\n"
	}
	return &runtime.CommandResult{Stdout: []byte(output)}, nil
}

func parseEchoOptions(args []string) (newline, escapes bool, remaining []string) {
	newline = true
	for len(args) > 0 && len(args[0]) > 1 && args[0][0] == '-' {
		valid := true
		for _, option := range args[0][1:] {
			if option != 'n' && option != 'e' && option != 'E' {
				valid = false
				break
			}
		}
		if !valid {
			break
		}
		for _, option := range args[0][1:] {
			switch option {
			case 'n':
				newline = false
			case 'e':
				escapes = true
			case 'E':
				escapes = false
			}
		}
		args = args[1:]
	}
	return newline, escapes, args
}

func decodeEchoEscapes(value string) (string, bool) {
	var output strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' || index+1 >= len(value) {
			output.WriteByte(value[index])
			continue
		}
		index++
		switch value[index] {
		case 'a':
			output.WriteByte('\a')
		case 'b':
			output.WriteByte('\b')
		case 'c':
			return output.String(), true
		case 'e', 'E':
			output.WriteByte(0x1b)
		case 'f':
			output.WriteByte('\f')
		case 'n':
			output.WriteByte('\n')
		case 'r':
			output.WriteByte('\r')
		case 't':
			output.WriteByte('\t')
		case 'v':
			output.WriteByte('\v')
		case '\\':
			output.WriteByte('\\')
		case 'x':
			start := index + 1
			end := min(start+2, len(value))
			wrote := false
			for end > start {
				parsed, err := strconv.ParseUint(value[start:end], 16, 8)
				if err == nil {
					output.WriteByte(byte(parsed))
					index = end - 1
					wrote = true
					break
				}
				end--
			}
			if !wrote {
				output.WriteString(`\x`)
			}
		case '0':
			start := index + 1
			end := min(start+3, len(value))
			wrote := false
			for end > start {
				parsed, err := strconv.ParseUint(value[start:end], 8, 8)
				if err == nil {
					output.WriteByte(byte(parsed))
					index = end - 1
					wrote = true
					break
				}
				end--
			}
			if !wrote {
				output.WriteByte(0)
			}
		default:
			output.WriteByte('\\')
			output.WriteByte(value[index])
		}
	}
	return output.String(), false
}
