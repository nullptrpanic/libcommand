// Package builtin provides safe, deterministic external commands simulated by
// the runtime without starting host processes.
package builtin

import (
	"maps"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

var definitions = make(map[string]*runtime.CommandDefinition)

func register(name string, command runtime.Command) {
	definitions[name] = &runtime.CommandDefinition{Command: command, Candidate: name != "*", Fallback: name == "*"}
}

func registerBuiltin(name string, command runtime.Command) {
	register(name, command)
	definitions[name].Builtin = true
	definitions[name].Candidate = false
}

func registerCommand(name string, command runtime.Command) {
	definitions[name] = &runtime.CommandDefinition{Command: command, Builtin: true}
}

func registerCandidate(name string, command runtime.Command) {
	definitions[name] = &runtime.CommandDefinition{Command: command, Builtin: true, Candidate: true}
}

func registerExternalCandidate(name string, command runtime.Command) {
	definitions[name] = &runtime.CommandDefinition{Command: command, Candidate: true}
}

func registerRestoreAlways(name string, command runtime.Command) {
	definitions[name] = &runtime.CommandDefinition{Command: command, Builtin: true, Candidate: true, RestoreAssignments: true}
}

// Definitions returns an isolated snapshot of all default commands.
func Definitions() map[string]*runtime.CommandDefinition {
	result := maps.Clone(definitions)
	for name, definition := range result {
		copied := *definition
		result[name] = &copied
	}
	return result
}

func concreteArguments(invocation *runtime.Invocation) ([]string, bool) {
	arguments := make([]string, len(invocation.Args))
	for index, argument := range invocation.Args {
		if argument.Kind != runtime.ArgumentString {
			return nil, false
		}
		arguments[index] = argument.Value
	}
	return arguments, true
}
