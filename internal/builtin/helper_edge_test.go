package builtin

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/expand"
)

func TestDeclarationParsingAndOptions(t *testing.T) {
	failure := &declarationError{message: "failure", exitCode: 2}
	if failure.Error() != "failure" {
		t.Fatalf("declaration error = %q", failure.Error())
	}
	spec := &declarationSpec{name: "declare"}
	for _, option := range []string{"-a", "-A", "-x", "-r"} {
		ended, err := applyDeclarationOption(spec, option)
		if ended || err != nil {
			t.Fatalf("option %q = %v, %v", option, ended, err)
		}
	}
	if spec.kind != expand.Associative || !spec.exported || !spec.readOnly {
		t.Fatalf("declaration spec = %#v", spec)
	}
	if ended, err := applyDeclarationOption(spec, "--"); !ended || err != nil {
		t.Fatalf("end options = %v, %v", ended, err)
	}
	if _, err := applyDeclarationOption(spec, "-Z"); err == nil {
		t.Fatal("unsupported declaration option succeeded")
	}

	plain, err := parseDeclarationAssignment("value")
	if err != nil || !plain.Naked || plain.Append || plain.Value != nil {
		t.Fatalf("plain declaration = %#v, %v", plain, err)
	}
	assigned, err := parseDeclarationAssignment("value=one")
	if err != nil || assigned.Naked || assigned.Append || assigned.Value.Lit() != "one" {
		t.Fatalf("assigned declaration = %#v, %v", assigned, err)
	}
	appended, err := parseDeclarationAssignment("value+=two")
	if err != nil || appended.Naked || !appended.Append || appended.Value.Lit() != "two" {
		t.Fatalf("appended declaration = %#v, %v", appended, err)
	}
	if _, err := parseDeclarationAssignment("bad-name=value"); err == nil {
		t.Fatal("invalid declaration identifier succeeded")
	}
}

func TestBuiltinClassificationHelpers(t *testing.T) {
	for _, name := range []string{"-e", "-f", "-c", "-r", "-w", "-x", "-d", "-s"} {
		if !isFileTestOperator(name) {
			t.Fatalf("file operator %q was rejected", name)
		}
	}
	if isFileTestOperator("-Z") {
		t.Fatal("unknown file operator was accepted")
	}
	if invertTestTruth(testTrue) != testFalse || invertTestTruth(testFalse) != testTrue || invertTestTruth(testUnknown) != testUnknown {
		t.Fatal("test truth inversion mismatch")
	}
	for kind, want := range map[runtime.CommandKind]string{
		runtime.CommandFunction: "function",
		runtime.CommandBuiltin:  "builtin",
		runtime.CommandFile:     "file",
		runtime.CommandMissing:  "",
	} {
		if got := commandKindName(kind); got != want {
			t.Fatalf("command kind %v = %q, want %q", kind, got, want)
		}
	}
}

func TestCDPathAndDirectoryVariableHelpers(t *testing.T) {
	for _, target := range []string{"name", "name/child"} {
		if !cdPathApplies(target) {
			t.Fatalf("CDPATH did not apply to %q", target)
		}
	}
	for _, target := range []string{"/absolute", "./relative", "../parent"} {
		if cdPathApplies(target) {
			t.Fatalf("CDPATH applied to %q", target)
		}
	}
	var assignments []string
	assign := func(name string, value *expand.Variable, unknown bool) error {
		assignments = append(assignments, name+"="+value.String())
		if name == "PWD" {
			return errors.New("readonly")
		}
		return nil
	}
	stderr := updateDirectoryVariables(assign, "/old", "/new", false, true)
	if !reflect.DeepEqual(assignments, []string{"OLDPWD=/old", "PWD=/new"}) || !strings.Contains(string(stderr), "readonly") {
		t.Fatalf("directory variables = %#v, stderr %q", assignments, stderr)
	}
}

func TestUnsupportedShellOptionStopsUnresolved(t *testing.T) {
	var calls [][]string
	err := executeBuiltinScript(t, `bash -Z; record after`, &runtime.Request{}, map[string]*runtime.CommandDefinition{"record": recordingCommand(&calls, nil)})
	if err == nil || !strings.Contains(err.Error(), "unsupported option") {
		t.Fatalf("unsupported shell option error = %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("unsupported shell option continued execution: %#v", calls)
	}
}
