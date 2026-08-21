package analysis

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

// Find detects deletion rooted at the filesystem root.
func Find(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	arguments, concrete := concreteArguments(invocation.Args)
	riskType := RiskType("")
	if concrete && findDeletesRoot(arguments, invocation.Dir, directoryUnresolved(invocation)) {
		riskType = RiskTypeDestructiveOperation
	}
	return commandResult(ctx, shell, invocation, riskType)
}

func findDeletesRoot(arguments []string, directory string, directoryUnknown bool) bool {
	deleteRequested := false
	for _, argument := range arguments {
		if argument == "-delete" {
			deleteRequested = true
			break
		}
	}
	if !deleteRequested {
		return false
	}

	searchPaths := findSearchPaths(arguments)
	if len(searchPaths) == 0 {
		searchPaths = []string{"."}
	}
	for _, searchPath := range searchPaths {
		resolved, known := resolvedPath(directory, searchPath, directoryUnknown)
		if known && resolved == "/" {
			return true
		}
	}
	return false
}

func findSearchPaths(arguments []string) []string {
	paths := make([]string, 0, len(arguments))
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "--":
			continue
		case argument == "-D":
			index++
			continue
		case argument == "-H" || argument == "-L" || argument == "-P" || strings.HasPrefix(argument, "-O"):
			continue
		case argument == "!" || argument == "(" || argument == ")" || argument == "," || strings.HasPrefix(argument, "-"):
			return paths
		default:
			paths = append(paths, argument)
		}
	}
	return paths
}
