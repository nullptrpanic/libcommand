package runtime

import "testing"

func TestChangeDirectoryPreservesRelativeUnknownState(t *testing.T) {
	s := newState(&Request{}, defaultMaxMemoryBytes)
	s.dir = newUnresolved("/unknown-base")
	s.vars.putUnknown("PWD", stringVariable(s.dir.data))

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
