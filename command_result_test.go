package libcommand

func commandResultForTest(command *CommandContext, stdout, stderr []byte, exitCode int) *CommandResult {
	return uncertainCommandResultForTest(command, stdout, stderr, exitCode, false, false, false)
}

func uncertainCommandResultForTest(command *CommandContext, stdout, stderr []byte, exitCode int, stdoutUnresolved, stderrUnresolved, exitUnresolved bool) *CommandResult {
	return command.Result(&CommandOutput{
		Stdout:   &Uncertain[[]byte]{Value: stdout, Unresolved: stdoutUnresolved},
		Stderr:   &Uncertain[[]byte]{Value: stderr, Unresolved: stderrUnresolved},
		ExitCode: &Uncertain[int]{Value: exitCode, Unresolved: exitUnresolved},
	})
}

func unresolvedCommandResultForTest(command *CommandContext) *CommandResult {
	return uncertainCommandResultForTest(command, nil, nil, 0, true, true, true)
}

func stoppedCommandResultForTest(command *CommandContext, stdout, stderr []byte, exitCode int) *CommandResult {
	result := commandResultForTest(command, stdout, stderr, exitCode)
	result.Action = CommandStop
	return result
}
