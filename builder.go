package libcommand

import (
	"github.com/nullptrpanic/libcommand/internal/builtin"
	"github.com/nullptrpanic/libcommand/internal/runtime"
)

// Builder registers commands before constructing a Simulator.
type Builder struct {
	commands map[string]Command
	limits   *Limits
}

// NewBuilder returns an empty Builder.
func NewBuilder() *Builder {
	return &Builder{
		commands: make(map[string]Command),
		limits:   limitsWithDefaults(nil),
	}
}

// Limits configures resource budgets for each simulation built by b. Nonzero
// values must be positive; trusted configuration is not validated by Builder.
func (b *Builder) Limits(limits *Limits) *Builder {
	b.limits = limitsWithDefaults(limits)
	return b
}

// Command registers a command for an exact expanded argv[0] value. The name "*"
// is the fallback used after exact lookup misses. A later registration with the
// same name replaces the earlier one, including literal declaration and let
// forms represented by specialized parser nodes. Shell control transfers named
// break, continue, return, and exit remain evaluator-owned and are not
// dispatched to registered commands.
func (b *Builder) Command(name string, command Command) *Builder {
	if b.commands == nil {
		b.commands = make(map[string]Command)
	}
	b.commands[name] = command
	return b
}

// Build returns an immutable Simulator.
func (b *Builder) Build() *Simulator {
	limits := limitsWithDefaults(b.limits)
	commands := builtin.Definitions()
	for name, command := range b.commands {
		definition := commands[name]
		if definition == nil {
			definition = &runtime.CommandDefinition{}
		}
		definition.Command = command
		definition.Candidate = name != "*"
		definition.RestoreAssignments = false
		definition.UserOverride = true
		definition.Fallback = name == "*"
		commands[name] = definition
	}
	return &Simulator{commands: commands, limits: limits}
}
