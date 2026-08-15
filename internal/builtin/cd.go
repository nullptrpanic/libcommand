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
		return shell.ResultUnknown(&runtime.CommandResult{ExitCode: 1}, false, true, false), nil
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
				return &runtime.CommandResult{Stderr: []byte("cd: invalid option\n"), ExitCode: 2}, nil
			}
			goto optionsDone
		}
	}

optionsDone:
	if len(args) > 1 {
		return &runtime.CommandResult{Stderr: []byte("cd: too many arguments\n"), ExitCode: 1}, nil
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
		return &runtime.CommandResult{Stderr: []byte("cd: HOME not set\n"), ExitCode: 1}, nil
	}
	if target == "-" {
		if shell.VariableUnknown("OLDPWD") {
			return shell.StopUnresolved("cd path depends on unresolved command output"), nil
		}
		oldPWD := shell.Variable("OLDPWD")
		if !oldPWD.IsSet() {
			return &runtime.CommandResult{Stderr: []byte("cd: OLDPWD not set\n"), ExitCode: 1}, nil
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
		} else if cdPathUnknown || shell.CandidateContext() {
			failure := shell.SnapshotForUnknownFailure()
			if err := shell.EnsureDirectory(resolved); err == nil {
				shell.SetDirectory(resolved, true)
				stderr := updateDirectoryVariables(shell, oldDirectory, resolved, true, true)
				shell.SetUnknownExitWithFailure(failure)
				return shell.ResultCurrentExit(&runtime.CommandResult{Stderr: stderr}, false, false), nil
			}
			message = "Not a directory"
		}
		return &runtime.CommandResult{Stderr: []byte(fmt.Sprintf("cd: %s: %s\n", target, message)), ExitCode: 1}, nil
	}
	shell.SetDirectory(resolved, resolvedUnknown)
	stderr := updateDirectoryVariables(shell, oldDirectory, resolved, oldUnknown, resolvedUnknown)
	if printDirectory {
		return &runtime.CommandResult{Stdout: []byte(resolved + "\n"), Stderr: stderr}, nil
	}
	return &runtime.CommandResult{Stderr: stderr}, nil
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

func updateDirectoryVariables(shell *runtime.CommandContext, oldDirectory, resolved string, oldUnknown, resolvedUnknown bool) []byte {
	var stderr []byte
	assign := func(name, value string, unknown bool) {
		variable := &expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: value}
		if err := shell.AssignVariable(name, variable, unknown); err != nil {
			stderr = append(stderr, fmt.Sprintf("cd: %v\n", err)...)
		}
	}
	assign("OLDPWD", oldDirectory, oldUnknown)
	assign("PWD", resolved, resolvedUnknown)
	return stderr
}
