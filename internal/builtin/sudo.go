package builtin

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/syntax"
)

func init() {
	registerExternalCandidate("sudo", executeSudo)
}

func executeSudo(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	arguments := invocation.Args
	user := "root"
	index := 0
	for index < len(arguments) {
		value, ok := wrapperArgument(invocation, index)
		if !ok {
			return unresolvedWrapper()
		}
		if value == "--" {
			index++
			break
		}
		if value == "-u" || value == "--user" || value == "-g" || value == "--group" || value == "-p" || value == "--prompt" || value == "-C" || value == "--close-from" || value == "-T" || value == "--command-timeout" || value == "-h" || value == "--host" {
			next, ok := wrapperArgument(invocation, index+1)
			if !ok {
				return unresolvedWrapper()
			}
			if value == "-u" || value == "--user" {
				user = next
			}
			index += 2
			continue
		}
		if strings.HasPrefix(value, "--user=") {
			user = strings.TrimPrefix(value, "--user=")
			index++
			continue
		}
		if strings.HasPrefix(value, "-u") && len(value) > 2 {
			user = value[2:]
			index++
			continue
		}
		if strings.HasPrefix(value, "--group=") || strings.HasPrefix(value, "--prompt=") || strings.HasPrefix(value, "--close-from=") || strings.HasPrefix(value, "--command-timeout=") || strings.HasPrefix(value, "--host=") || strings.HasPrefix(value, "--preserve-env=") {
			index++
			continue
		}
		if value == "-A" || value == "--askpass" || value == "-b" || value == "--background" || value == "-E" || value == "--preserve-env" || value == "-H" || value == "--set-home" || value == "-n" || value == "--non-interactive" || value == "-P" || value == "--preserve-groups" || value == "-S" || value == "--stdin" {
			index++
			continue
		}
		if shortFlagsOnly(value, "AbEHnPS") {
			index++
			continue
		}
		if value == "-i" || value == "--login" || value == "-s" || value == "--shell" || value == "-D" || value == "--chdir" || value == "-R" || value == "--chroot" || value == "-l" || value == "--list" || value == "-v" || value == "--validate" || value == "-k" || value == "--reset-timestamp" || value == "-K" || value == "--remove-timestamp" || value == "-V" || value == "--version" || value == "--help" {
			return unresolvedWrapper()
		}
		if strings.HasPrefix(value, "-") {
			return unresolvedWrapper()
		}
		break
	}

	assignments := make(map[string]string)
	for index < len(arguments) {
		value, ok := wrapperArgument(invocation, index)
		if !ok {
			return unresolvedWrapper()
		}
		name, value, assignment := strings.Cut(value, "=")
		if !assignment || !syntax.ValidName(name) {
			break
		}
		assignments[name] = value
		index++
	}
	if user == "" {
		return unresolvedWrapper()
	}
	if err := shell.ChangeUser(user); err != nil {
		return nil, err
	}
	return invokeExternalWrapper(shell, invocation, index, assignments)
}
