package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

const getoptsStateName = "getopts"

func init() {
	registerCommand("getopts", executeGetopts)
}

func executeGetopts(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	if len(args) < 2 {
		return commandResult(shell, nil, []byte("getopts: usage: getopts optstring name [arg ...]\n"), 2), nil
	}
	optionString, variableName := args[0], args[1]
	finish := func(output *runtime.CommandOutput) *runtime.CommandResult {
		if !syntax.ValidName(variableName) {
			return commandResult(shell, nil, []byte(fmt.Sprintf("getopts: `%s': not a valid identifier\n", variableName)), 1)
		}
		if shell.Variable(variableName).ReadOnly {
			return commandResult(shell, nil, []byte(fmt.Sprintf("getopts: %s: readonly variable\n", variableName)), 1)
		}
		return shell.Result(output)
	}
	if shell.VariableUnknown("OPTIND") {
		markGetoptsStateUnknown(shell, variableName)
		return finish(uncertainCommandOutput(nil, nil, 0, false, true, true)), nil
	}

	positional := args[2:]
	if len(positional) == 0 {
		positional = shell.PositionalArguments()
	}
	optind, err := strconv.Atoi(shell.Variable("OPTIND").String())
	if err != nil || optind < 1 {
		optind = 1
	}
	state := loadGetoptsState(shell)
	version := shell.VariableVersion("OPTIND")
	if state.optindVersion != version || !state.optindWriteFailed && state.index != optind {
		state.index = optind
		state.offset = 1
		state.optindVersion = version
		state.optindWriteFailed = false
	}
	if state.offset < 1 {
		state.offset = 1
	}

	for {
		if state.index > len(positional) {
			setGetoptsIndex(shell, state)
			unsetGetoptsValue(shell, "OPTARG")
			return finish(commandOutput(nil, nil, 1)), nil
		}
		token := positional[state.index-1]
		if token == "--" {
			state.index++
			state.offset = 1
			setGetoptsIndex(shell, state)
			unsetGetoptsValue(shell, "OPTARG")
			return finish(commandOutput(nil, nil, 1)), nil
		}
		if len(token) < 2 || token[0] != '-' {
			setGetoptsIndex(shell, state)
			unsetGetoptsValue(shell, "OPTARG")
			return finish(commandOutput(nil, nil, 1)), nil
		}
		if state.offset >= len(token) {
			state.index++
			state.offset = 1
			continue
		}

		option := token[state.offset]
		state.offset++
		trimmed := strings.TrimPrefix(optionString, ":")
		position := strings.IndexByte(trimmed, option)
		silent := strings.HasPrefix(optionString, ":")
		if position < 0 || option == ':' {
			finishGetoptsToken(shell, state, token)
			setGetoptsValue(shell, variableName, "?")
			if silent {
				setGetoptsValue(shell, "OPTARG", string(option))
				return finish(commandOutput(nil, nil, 0)), nil
			}
			unsetGetoptsValue(shell, "OPTARG")
			return finish(commandOutput(nil, []byte(fmt.Sprintf("getopts: illegal option -- %c\n", option)), 0)), nil
		}

		requiresArgument := position+1 < len(trimmed) && trimmed[position+1] == ':'
		if !requiresArgument {
			finishGetoptsToken(shell, state, token)
			setGetoptsValue(shell, variableName, string(option))
			unsetGetoptsValue(shell, "OPTARG")
			return finish(commandOutput(nil, nil, 0)), nil
		}

		argument := ""
		if state.offset < len(token) {
			argument = token[state.offset:]
			state.index++
			state.offset = 1
		} else if state.index < len(positional) {
			state.index++
			argument = positional[state.index-1]
			state.index++
			state.offset = 1
		} else {
			state.index++
			state.offset = 1
			setGetoptsIndex(shell, state)
			if silent {
				setGetoptsValue(shell, variableName, ":")
				setGetoptsValue(shell, "OPTARG", string(option))
				return finish(commandOutput(nil, nil, 0)), nil
			}
			setGetoptsValue(shell, variableName, "?")
			unsetGetoptsValue(shell, "OPTARG")
			return finish(commandOutput(nil, []byte(fmt.Sprintf("getopts: option requires an argument -- %c\n", option)), 0)), nil
		}
		setGetoptsIndex(shell, state)
		setGetoptsValue(shell, variableName, string(option))
		setGetoptsValue(shell, "OPTARG", argument)
		return finish(commandOutput(nil, nil, 0)), nil
	}
}

type getoptsState struct {
	index             int
	offset            int
	optindVersion     uint64
	optindWriteFailed bool
}

func loadGetoptsState(shell *runtime.CommandContext) *getoptsState {
	values := shell.CommandState(getoptsStateName)
	state := getoptsState{index: 1, offset: 1}
	if len(values) == 4 {
		state.index = int(values[0])
		state.offset = int(values[1])
		state.optindVersion = values[2]
		state.optindWriteFailed = values[3] != 0
	}
	return &state
}

func storeGetoptsState(shell *runtime.CommandContext, state *getoptsState) {
	writeFailed := uint64(0)
	if state.optindWriteFailed {
		writeFailed = 1
	}
	shell.SetCommandState(getoptsStateName, []uint64{uint64(state.index), uint64(state.offset), state.optindVersion, writeFailed})
}

func finishGetoptsToken(shell *runtime.CommandContext, state *getoptsState, token string) {
	if state.offset >= len(token) {
		state.index++
		state.offset = 1
	}
	setGetoptsIndex(shell, state)
}

func setGetoptsIndex(shell *runtime.CommandContext, state *getoptsState) {
	value := &expand.Variable{Set: true, Kind: expand.String, Str: strconv.Itoa(state.index)}
	state.optindWriteFailed = shell.AssignVariable("OPTIND", value, false) != nil
	state.optindVersion = shell.VariableVersion("OPTIND")
	storeGetoptsState(shell, state)
}

func setGetoptsValue(shell *runtime.CommandContext, name, value string) {
	_ = shell.AssignVariable(name, &expand.Variable{Set: true, Kind: expand.String, Str: value}, false)
}

func unsetGetoptsValue(shell *runtime.CommandContext, name string) {
	_ = shell.UnsetVariable(name)
}

func markGetoptsStateUnknown(shell *runtime.CommandContext, variableName string) {
	for _, name := range []string{"OPTIND", "OPTARG", variableName} {
		value := shell.Variable(name)
		if !value.Declared() {
			value = &expand.Variable{Set: true, Kind: expand.String}
		}
		_ = shell.AssignVariable(name, value, true)
	}
}
