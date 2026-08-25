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
			return command.Result(command.Output().
				Stderr(Unresolved[[]byte](nil)).
				ExitCode(Resolved(1)).
				Build()), nil
		}
	}

	name := invocation.Name
	state := command.State()
	if len(arguments) > 1 {
		if name == "exit" || name == "return" || (name == "break" || name == "continue") && state.loopDepth > 0 {
			state.signal = signalExit
		}
		return command.Result(command.Output().
			Stderr(Resolved([]byte(name + ": too many arguments\n"))).
			ExitCode(Resolved(1)).
			Build()), nil
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
			return command.Result(command.Output().
				Stderr(Resolved([]byte(name + ": numeric argument required\n"))).
				ExitCode(Resolved(2)).
				Build()), nil
		}
		if name == "return" || name == "exit" {
			argument = int(value & 255)
		} else if value > int64(^uint(0)>>1) {
			argument = int(^uint(0) >> 1)
		} else {
			argument = int(value)
		}
	}

	var stderr []byte
	exitCode := argument
	switch name {
	case "break", "continue":
		if state.loopDepth == 0 {
			stderr = []byte(name + ": only meaningful in a loop\n")
			exitCode = 1
			break
		}
		if argument == 0 {
			stderr = []byte(name + ": 0: loop count out of range\n")
			exitCode = 2
			break
		}
		state.signal = signalBreak
		if name == "continue" {
			state.signal = signalContinue
		}
		state.signalDepth = min(argument, state.loopDepth)
		exitCode = 0
	case "return":
		if state.funcDepth == 0 && state.sourceDepth == 0 {
			stderr = []byte("return: can only be used in a function\n")
			exitCode = 1
			break
		}
		state.signal = signalReturn
	case "exit":
		state.signal = signalExit
	}
	return command.Result(command.Output().
		Stderr(Resolved(stderr)).
		ExitCode(newUncertain(exitCode, preserveUnknown)).
		Build()), nil
}
