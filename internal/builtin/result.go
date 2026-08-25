package builtin

import "github.com/nullptrpanic/libcommand/internal/runtime"

func commandResult(shell *runtime.CommandContext, stdout, stderr []byte, exitCode int) *runtime.CommandResult {
	return shell.Result(commandOutput(stdout, stderr, exitCode))
}

func uncertainCommandResult(shell *runtime.CommandContext, stdout, stderr []byte, exitCode int, stdoutUnresolved, stderrUnresolved, exitUnresolved bool) *runtime.CommandResult {
	return shell.Result(uncertainCommandOutput(stdout, stderr, exitCode, stdoutUnresolved, stderrUnresolved, exitUnresolved))
}

func unresolvedCommandResult(shell *runtime.CommandContext) *runtime.CommandResult {
	return shell.Result(unresolvedCommandOutput())
}

func unresolvedStderrCommandResult(shell *runtime.CommandContext, exitCode int) *runtime.CommandResult {
	return uncertainCommandResult(shell, nil, nil, exitCode, false, true, false)
}

func unresolvedExitCommandResult(shell *runtime.CommandContext, exitCode int) *runtime.CommandResult {
	return uncertainCommandResult(shell, nil, nil, exitCode, false, false, true)
}

func commandOutput(stdout, stderr []byte, exitCode int) *runtime.CommandOutput {
	return uncertainCommandOutput(stdout, stderr, exitCode, false, false, false)
}

func uncertainCommandOutput(stdout, stderr []byte, exitCode int, stdoutUnresolved, stderrUnresolved, exitUnresolved bool) *runtime.CommandOutput {
	return &runtime.CommandOutput{
		Stdout:   uncertainValue(stdout, stdoutUnresolved),
		Stderr:   uncertainValue(stderr, stderrUnresolved),
		ExitCode: uncertainValue(exitCode, exitUnresolved),
	}
}

func unresolvedCommandOutput() *runtime.CommandOutput {
	return uncertainCommandOutput(nil, nil, 0, true, true, true)
}

func uncertainValue[T any](value T, unresolved bool) *runtime.Uncertain[T] {
	return &runtime.Uncertain[T]{Value: value, Unresolved: unresolved}
}
