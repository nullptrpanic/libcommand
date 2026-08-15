package builtin

import (
	"context"
	"testing"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func TestDefaultRegistry(t *testing.T) {
	commands := Definitions()
	if commands["seq"] == nil || commands["*"] == nil || commands["missing"] != nil {
		t.Fatalf("definitions = %#v", commands)
	}
	delete(commands, "seq")
	if current := Definitions()["seq"]; current == nil {
		t.Fatal("Definitions returned mutable registry state")
	}
}

func TestDefaultRegistryOwnsEveryRoutedCallCommand(t *testing.T) {
	commands := Definitions()
	for _, name := range []string{
		"*", ":", "[", ".", "alias", "base64", "bash", "bind",
		"builtin", "caller", "cd", "command", "compgen", "complete", "compopt",
		"declare", "dirs", "disown", "echo", "enable", "env", "eval",
		"exec", "export", "false", "fc", "getopts", "hash", "help",
		"history", "jobs", "kill", "let", "local", "logout", "mapfile", "popd",
		"printf", "pushd", "pwd", "read", "readarray", "readonly", "rev",
		"seq", "set", "sh", "shift", "shopt", "source", "suspend", "test", "times",
		"trap", "true", "type", "typeset", "ulimit", "umask", "unalias", "unset",
		"wait",
	} {
		if commands[name] == nil {
			t.Errorf("default command %q is not owned by builtin registry", name)
		}
	}
	for _, name := range []string{".", "builtin", "cd", "command", "declare", "echo", "eval", "exec", "export", "getopts", "let", "local", "printf", "read", "set", "shopt", "test", "trap", "type", "unset", "wait"} {
		if !commands[name].Builtin {
			t.Errorf("default command %q is not marked as a builtin", name)
		}
	}
	for _, name := range []string{"break", "continue", "return", "exit"} {
		if commands[name] != nil {
			t.Errorf("language control command %q is owned by builtin registry", name)
		}
	}
	for _, name := range []string{".", "bash", "builtin", "command", "env", "eval", "exec", "sh", "source", "base64", "rev", "seq"} {
		if !commands[name].Candidate {
			t.Errorf("default command %q is not marked as a candidate", name)
		}
	}
	for _, name := range []string{"bash", "env", "sh"} {
		if commands[name].Builtin {
			t.Errorf("external wrapper %q is marked as a builtin", name)
		}
	}
}

func TestDefaultFallbackIsUnresolved(t *testing.T) {
	result, err := executeFallback(context.Background(), nil, &runtime.Invocation{Name: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Unresolved {
		t.Fatalf("fallback result = %#v, want unresolved", result)
	}
}
