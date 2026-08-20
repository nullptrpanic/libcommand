package analysis

import (
	"context"
	"path"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

// RM detects recursive removal of the filesystem root.
func RM(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	arguments, concrete := concreteArguments(invocation.Args)
	reason := ""
	if concrete && recursiveRootRemoval(arguments, invocation.Dir) {
		reason = "recursive removal of the filesystem root"
	}
	return commandResult(ctx, shell, invocation, reason)
}

func recursiveRootRemoval(arguments []string, directory string) bool {
	recursive := false
	options := true
	var targets []string
	for _, argument := range arguments {
		if options && argument == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(argument, "--") {
			if argument == "--recursive" {
				recursive = true
			}
			continue
		}
		if options && len(argument) > 1 && argument[0] == '-' {
			if strings.ContainsAny(argument[1:], "rR") {
				recursive = true
			}
			continue
		}
		targets = append(targets, argument)
	}
	if !recursive {
		return false
	}
	for _, target := range targets {
		resolved := target
		if !path.IsAbs(resolved) {
			resolved = path.Join(directory, resolved)
		}
		resolved = path.Clean(resolved)
		if resolved == "/" || rootGlob(resolved) {
			return true
		}
	}
	return false
}

func rootGlob(target string) bool {
	if path.Dir(target) != "/" {
		return false
	}
	return strings.ContainsAny(path.Base(target), "*?[")
}
