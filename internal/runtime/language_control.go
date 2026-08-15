package runtime

import (
	"context"
	"strconv"
)

var languageControlDefinition = &CommandDefinition{
	Command: executeLanguageControl,
	Builtin: true,
}

func lookupLanguageControl(name string) *CommandDefinition {
	switch name {
	case "break", "continue", "return", "exit":
		return languageControlDefinition
	default:
		return nil
	}
}

func executeLanguageControl(_ context.Context, command *CommandContext, invocation *Invocation) (*CommandResult, error) {
	arguments := invocation.Args
	for _, argument := range arguments {
		if argument.Kind != ArgumentString {
			return command.ResultUnknown(&CommandResult{ExitCode: 1}, false, true, false), nil
		}
	}

	name := invocation.Name
	state := command.State()
	if len(arguments) > 1 {
		if name == "exit" || name == "return" || (name == "break" || name == "continue") && state.loopDepth > 0 {
			state.signal = signalExit
		}
		return &CommandResult{Stderr: []byte(name + ": too many arguments\n"), ExitCode: 1}, nil
	}

	argument := 1
	preserveUnknown := false
	if name == "return" || name == "exit" {
		argument, preserveUnknown = state.exitStatus.Data()
		preserveUnknown = len(arguments) == 0 && preserveUnknown && (name == "exit" || state.funcDepth > 0 || state.sourceDepth > 0)
	}
	if len(arguments) == 1 {
		value, err := strconv.ParseInt(arguments[0].Value, 10, 64)
		if err != nil || (name == "break" || name == "continue") && value < 0 {
			switch name {
			case "exit":
				state.signal = signalExit
			case "return":
				if state.funcDepth != 0 || state.sourceDepth != 0 {
					state.signal = signalReturn
				}
			case "break", "continue":
				if state.loopDepth > 0 {
					state.signal = signalExit
				}
			}
			return &CommandResult{Stderr: []byte(name + ": numeric argument required\n"), ExitCode: 2}, nil
		}
		if name == "return" || name == "exit" {
			argument = int(value & 255)
		} else if value > int64(^uint(0)>>1) {
			argument = int(^uint(0) >> 1)
		} else {
			argument = int(value)
		}
	}

	result := &CommandResult{ExitCode: argument}
	switch name {
	case "break", "continue":
		if state.loopDepth == 0 {
			result.Stderr = []byte(name + ": only meaningful in a loop\n")
			result.ExitCode = 1
			return result, nil
		}
		if argument == 0 {
			result.Stderr = []byte(name + ": 0: loop count out of range\n")
			result.ExitCode = 2
			return result, nil
		}
		state.signal = signalBreak
		if name == "continue" {
			state.signal = signalContinue
		}
		state.signalDepth = min(argument, state.loopDepth)
		result.ExitCode = 0
	case "return":
		if state.funcDepth == 0 && state.sourceDepth == 0 {
			result.Stderr = []byte("return: can only be used in a function\n")
			result.ExitCode = 1
			return result, nil
		}
		state.signal = signalReturn
	case "exit":
		state.signal = signalExit
	}
	return command.ResultUnknown(result, false, false, preserveUnknown), nil
}
