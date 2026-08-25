package libcommand_test

import "github.com/nullptrpanic/libcommand"

func externalCommandResult(command *libcommand.CommandContext, stdout, stderr []byte, exitCode int) *libcommand.CommandResult {
	return externalUncertainCommandResult(command, stdout, stderr, exitCode, false, false, false)
}

func externalUncertainCommandResult(command *libcommand.CommandContext, stdout, stderr []byte, exitCode int, stdoutUnresolved, stderrUnresolved, exitUnresolved bool) *libcommand.CommandResult {
	return command.Result(&libcommand.CommandOutput{
		Stdout:   &libcommand.Uncertain[[]byte]{Value: stdout, Unresolved: stdoutUnresolved},
		Stderr:   &libcommand.Uncertain[[]byte]{Value: stderr, Unresolved: stderrUnresolved},
		ExitCode: &libcommand.Uncertain[int]{Value: exitCode, Unresolved: exitUnresolved},
	})
}

func externalUnresolvedCommandResult(command *libcommand.CommandContext) *libcommand.CommandResult {
	return externalUncertainCommandResult(command, nil, nil, 0, true, true, true)
}
