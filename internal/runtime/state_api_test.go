package runtime

import (
	"strings"
	"testing"
)

func TestChangeDirectoryPreservesRelativeUnknownState(t *testing.T) {
	s := newState(&Request{}, defaultMaxMemoryBytes)
	s.dir = newUnresolved("/unknown-base")
	s.vars.putUnknown("PWD", stringVariable(s.dir.Value))

	if err := s.ChangeDirectory("child"); err != nil {
		t.Fatal(err)
	}
	if directory := s.Directory(); directory != "" {
		t.Fatalf("relative directory = %q, want unresolved", directory)
	}
	if value, exists, resolved := s.Variable("PWD"); value != "" || !exists || resolved {
		t.Fatalf("relative PWD = %q, exists %v, resolved %v", value, exists, resolved)
	}

	if err := s.ChangeDirectory("/absolute"); err != nil {
		t.Fatal(err)
	}
	if directory := s.Directory(); directory != "/absolute" {
		t.Fatalf("absolute directory = %q", directory)
	}
	if value, exists, resolved := s.Variable("PWD"); value != "/absolute" || !exists || !resolved {
		t.Fatalf("absolute PWD = %q, exists %v, resolved %v", value, exists, resolved)
	}
}

func TestChangeDirectoryRejectsOversizedRelativeInputBeforeCleaning(t *testing.T) {
	const maximum = 64
	s := newState(&Request{}, maximum)
	name := strings.Repeat("a/../", maximum)

	if err := s.ChangeDirectory(name); err == nil {
		t.Fatal("ChangeDirectory() accepted an input larger than the materialization limit")
	}
	if directory := s.Directory(); directory != "/" {
		t.Fatalf("directory = %q, want unchanged root", directory)
	}
}
