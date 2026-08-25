package builtin

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/expand"
)

func init() {
	registerCommand("cd", executeCD)
}

func executeCD(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	for len(args) > 0 {
		switch args[0] {
		case "-L", "-P":
			args = args[1:]
		case "--":
			args = args[1:]
			goto optionsDone
		default:
			if strings.HasPrefix(args[0], "-") && args[0] != "-" {
				return commandResult(shell, nil, []byte("cd: invalid option\n"), 2), nil
			}
			goto optionsDone
		}
	}

optionsDone:
	if len(args) > 1 {
		return commandResult(shell, nil, []byte("cd: too many arguments\n"), 1), nil
	}
	target := ""
	printDirectory := false
	if len(args) == 1 {
		target = args[0]
	} else if home := shell.Variable("HOME"); home.IsSet() {
		if shell.VariableUnknown("HOME") {
			return shell.StopUnresolved("cd path depends on unresolved command output"), nil
		}
		target = home.String()
	} else {
		return commandResult(shell, nil, []byte("cd: HOME not set\n"), 1), nil
	}
	if target == "-" {
		if shell.VariableUnknown("OLDPWD") {
			return shell.StopUnresolved("cd path depends on unresolved command output"), nil
		}
		oldPWD := shell.Variable("OLDPWD")
		if !oldPWD.IsSet() {
			return commandResult(shell, nil, []byte("cd: OLDPWD not set\n"), 1), nil
		}
		target = oldPWD.String()
		printDirectory = true
	}
	oldDirectory, oldUnknown := shell.Directory()
	resolved := ""
	resolvedUnknown := false
	cdPathUnknown := cdPathApplies(target) && shell.VariableUnknown("CDPATH")
	if cdPathApplies(target) && !cdPathUnknown {
		if candidate, unknown, found := resolveCDPath(shell, target); found {
			resolved = candidate
			resolvedUnknown = unknown
			printDirectory = true
		}
	}
	if resolved == "" {
		resolved = shell.ResolvePath(target)
		resolvedUnknown = cdPathUnknown || oldUnknown && !path.IsAbs(target)
	}
	kind := shell.PathKind(resolved)
	if kind != runtime.PathDirectory {
		message := "No such file or directory"
		if kind != runtime.PathMissing {
			message = "Not a directory"
		} else {
			success := shell.ForkState()
			failure := shell.ForkState()
			if err := success.EnsureDirectory(resolved); err == nil {
				if err := success.SetDirectory(resolved, true); err != nil {
					return nil, err
				}
				stderr := updateDirectoryVariables(success.AssignVariable, oldDirectory, resolved, true, true)
				successOutput := shell.Output().Stderr(runtime.Resolved(stderr))
				if printDirectory {
					successOutput.Stdout(uncertainValue([]byte(resolved+"\n"), resolvedUnknown))
				}
				result := shell.NewResult()
				result.AddOutput(success, successOutput.Build())
				result.AddOutput(failure, shell.Output().
					Stderr(runtime.Resolved([]byte(fmt.Sprintf("cd: %s: %s\n", target, message)))).
					ExitCode(runtime.Resolved(1)).
					Build())
				return result, nil
			}
			message = "Not a directory"
		}
		return commandResult(shell, nil, []byte(fmt.Sprintf("cd: %s: %s\n", target, message)), 1), nil
	}
	if err := shell.SetDirectory(resolved, resolvedUnknown); err != nil {
		return nil, err
	}
	stderr := updateDirectoryVariables(shell.AssignVariable, oldDirectory, resolved, oldUnknown, resolvedUnknown)
	if printDirectory {
		return commandResult(shell, []byte(resolved+"\n"), stderr, 0), nil
	}
	return commandResult(shell, nil, stderr, 0), nil
}

func cdPathApplies(target string) bool {
	if path.IsAbs(target) {
		return false
	}
	first, _, _ := strings.Cut(target, "/")
	return first != "." && first != ".."
}

func resolveCDPath(shell *runtime.CommandContext, target string) (string, bool, bool) {
	cdPath := shell.Variable("CDPATH")
	if !cdPath.IsSet() || cdPath.String() == "" {
		return "", false, false
	}
	_, directoryUnknown := shell.Directory()
	for _, directory := range strings.Split(cdPath.String(), ":") {
		if directory == "" {
			continue
		}
		candidate := shell.ResolvePath(path.Join(directory, target))
		if shell.PathKind(candidate) == runtime.PathDirectory {
			return candidate, directoryUnknown && !path.IsAbs(directory), true
		}
	}
	return "", false, false
}

func updateDirectoryVariables(assignVariable func(string, *expand.Variable, bool) error, oldDirectory, resolved string, oldUnknown, resolvedUnknown bool) []byte {
	var stderr []byte
	assign := func(name, value string, unknown bool) {
		variable := &expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: value}
		if err := assignVariable(name, variable, unknown); err != nil {
			stderr = append(stderr, fmt.Sprintf("cd: %v\n", err)...)
		}
	}
	assign("OLDPWD", oldDirectory, oldUnknown)
	assign("PWD", resolved, resolvedUnknown)
	return stderr
}
