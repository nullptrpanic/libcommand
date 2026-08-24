package runtime

import (
	"errors"
	"path"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/expand"
)

var errFrozenState = errors.New("parent state is immutable")

// PathID returns the nonzero execution-path identity. IDs are unique within
// one simulation.
func (s *State) PathID() uint64 {
	return s.pathID
}

// Parent returns the frozen state from which this path forked. The root path
// returns nil.
func (s *State) Parent() *State {
	return s.parent
}

// User returns the simulated current user.
func (s *State) User() string {
	return s.user
}

// Directory returns the current working directory. It returns an empty string
// when the directory depends on unresolved shell state.
func (s *State) Directory() string {
	directory, unresolved := s.dir.Data()
	if unresolved {
		return ""
	}
	return directory
}

// ChangeDirectory changes the current working directory and updates PWD and
// OLDPWD atomically. The path is resolved against the current directory; it is
// not checked against the simulator's virtual filesystem.
func (s *State) ChangeDirectory(name string) error {
	maximum := s.maximumMemoryBytes()
	estimated, ok := materialize.Add(0, len(name), maximum)
	if !ok {
		return materialize.LimitError(maximum)
	}
	directory, directoryUnresolved := s.dir.Data()
	if !path.IsAbs(name) {
		if _, ok = materialize.Add(estimated, len(directory)+1, maximum); !ok {
			return materialize.LimitError(maximum)
		}
	}

	resolved := s.fs.resolve(directory, name)
	resolvedUnresolved := directoryUnresolved && !path.IsAbs(name)
	return s.mutate(func() error {
		if err := assignShellVariable(s, "OLDPWD", directoryVariable(directory), directoryUnresolved); err != nil {
			return err
		}
		if err := assignShellVariable(s, "PWD", directoryVariable(resolved), resolvedUnresolved); err != nil {
			return err
		}
		if resolvedUnresolved {
			s.dir = newUnresolved(resolved)
		} else {
			s.dir = newCertain(resolved)
		}
		return nil
	})
}

// Variable returns one shell variable as a string. exists reports whether the
// variable is set, and resolved reports whether its value is concrete.
func (s *State) Variable(name string) (value string, exists bool, resolved bool) {
	variable, exists := s.vars.lookup(name)
	if !exists || !variable.IsSet() {
		return "", false, true
	}
	if s.vars.isUnknown(name) {
		return "", true, false
	}
	return variable.String(), true, true
}

// SetVariable assigns a concrete string shell variable atomically.
func (s *State) SetVariable(name, value string) error {
	maximum := s.maximumMemoryBytes()
	variable := stringVariable(value)
	if variableEntryBytes(name, &variable) > maximum {
		return materialize.LimitError(maximum)
	}
	return s.mutate(func() error {
		return assignShellVariable(s, name, variable, false)
	})
}

// UnsetVariable removes one shell variable atomically.
func (s *State) UnsetVariable(name string) error {
	return s.mutate(func() error {
		return unsetShellVariable(s, name)
	})
}

func stringVariable(value string) expand.Variable {
	return expand.Variable{Set: true, Kind: expand.String, Str: value}
}

func directoryVariable(value string) expand.Variable {
	return expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: value}
}

func (s *State) maximumMemoryBytes() int {
	return normalizedMaxMemoryBytes(s.fs.maximumBytes)
}

func (s *State) mutate(change func() error) (err error) {
	if s.frozen {
		return errFrozenState
	}
	originalVariables := s.vars
	originalDirectory := s.dir
	s.vars = s.vars.clone()
	defer func() {
		if err != nil {
			s.vars = originalVariables
			s.dir = originalDirectory
		}
	}()
	if err = change(); err == nil {
		err = s.checkPublicMutationMaterialization()
	}
	return err
}

func (s *State) checkPublicMutationMaterialization() error {
	maximum := s.maximumMemoryBytes()
	total, ok := stateMaterialization(s)
	if !ok {
		return materialize.LimitError(maximum)
	}
	if total > s.initialBytes {
		total -= s.initialBytes
	} else {
		total = 0
	}
	if total > maximum {
		return materialize.LimitError(maximum)
	}
	return nil
}
