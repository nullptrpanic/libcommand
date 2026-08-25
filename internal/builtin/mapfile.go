package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func init() {
	registerCommand("mapfile", executeMapfile)
}

func executeMapfile(ctx context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	name := invocation.Name
	originalInput, originalUnresolved := shell.Input()
	restoreFailure := func(err error) (*runtime.CommandResult, error) {
		shell.SetInput(originalInput, originalUnresolved)
		return nil, err
	}
	trimDelimiter := false
	arrayName := "MAPFILE"
	maximum := 0
	skip := 0
	origin := 0
	originSet := false
	delimiter := byte('\n')
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "-t":
			trimDelimiter = true
		case "--":
			if index+1 < len(args) {
				arrayName = args[index+1]
			}
			index = len(args)
		case "-n", "-O", "-s":
			index++
			if index >= len(args) {
				return commandResult(shell, nil, []byte(name+": option requires an argument\n"), 2), nil
			}
			value, valid := parseNonNegative(args[index])
			if !valid {
				return commandResult(shell, nil, []byte(name+": invalid numeric argument\n"), 2), nil
			}
			switch args[index-1] {
			case "-n":
				maximum = value
			case "-O":
				origin = value
				originSet = true
			case "-s":
				skip = value
			}
		case "-d":
			index++
			if index >= len(args) {
				return commandResult(shell, nil, []byte(name+": option requires an argument\n"), 2), nil
			}
			if args[index] == "" {
				delimiter = 0
			} else {
				delimiter = args[index][0]
			}
		default:
			if strings.HasPrefix(args[index], "-") {
				return commandResult(shell, nil, []byte(name+": invalid option\n"), 2), nil
			}
			arrayName = args[index]
		}
	}
	if !syntax.ValidName(arrayName) {
		return commandResult(shell, nil, []byte(fmt.Sprintf("%s: `%s': not a valid identifier\n", name, arrayName)), 1), nil
	}
	if shell.Variable(arrayName).ReadOnly {
		return commandResult(shell, nil, []byte(fmt.Sprintf("%s: %s: readonly variable\n", name, arrayName)), 1), nil
	}
	_, inputUnknown := shell.Input()
	if inputUnknown {
		if err := shell.AssignVariable(arrayName, &expand.Variable{Set: true, Kind: expand.Indexed}, true); err != nil {
			return restoreFailure(err)
		}
		shell.SetInput(nil, false)
		return unresolvedExitCommandResult(shell, 0), nil
	}
	for range skip {
		if err := ctx.Err(); err != nil {
			return restoreFailure(err)
		}
		if line, terminated := consumeInputRecord(shell, delimiter); line == "" && !terminated {
			break
		}
	}
	lines := make([]string, 0)
	materializedBytes := 0
	for maximum == 0 || len(lines) < maximum {
		if len(lines)%256 == 0 {
			if err := ctx.Err(); err != nil {
				return restoreFailure(err)
			}
		}
		line, terminated := consumeInputRecord(shell, delimiter)
		if line == "" && !terminated {
			break
		}
		if !trimDelimiter && terminated {
			line += string(delimiter)
		}
		var err error
		materializedBytes, err = addMaterializedString(materializedBytes, line, shell.MaxMemoryBytes())
		if err != nil {
			return restoreFailure(err)
		}
		lines = append(lines, line)
		if !terminated {
			break
		}
	}
	if origin+len(lines) < origin {
		return restoreFailure(fmt.Errorf("%s: array index overflow", name))
	}
	current := shell.Variable(arrayName)
	length := origin + len(lines)
	var indexedSlots map[int]struct{}
	if originSet {
		indexedSlots = make(map[int]struct{})
		if current.Kind == expand.Indexed {
			length = max(length, len(current.List))
			indexedSlots = explicitIndexedSlots(shell.IndexedSlots(arrayName), len(current.List))
		}
	}
	if length < 0 || length > shell.MaxMemoryBytes()/materialize.EntryBytes {
		return restoreFailure(materialize.LimitError(shell.MaxMemoryBytes()))
	}
	values := make([]string, length)
	if originSet && current.Kind == expand.Indexed {
		copy(values, current.List)
	}
	copy(values[origin:], lines)
	if indexedSlots != nil {
		for index := range lines {
			indexedSlots[origin+index] = struct{}{}
		}
		indexedSlots = normalizeIndexedSlots(indexedSlots, len(values))
	}
	value := &expand.Variable{Set: true, Kind: expand.Indexed, List: values}
	if err := shell.AssignIndexedVariable(arrayName, value, false, indexedSlots); err != nil {
		return restoreFailure(err)
	}
	return commandResult(shell, nil, nil, 0), nil
}
