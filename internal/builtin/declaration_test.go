package builtin

import (
	"testing"

	"mvdan.cc/sh/v3/expand"
)

func TestDeclarationOptionParsing(t *testing.T) {
	spec := &declarationSpec{name: "declare", kind: expand.Unknown}
	if ended, err := applyDeclarationOption(spec, "-axr"); err != nil || ended {
		t.Fatalf("applyDeclarationOption() = %t, %v", ended, err)
	}
	if spec.kind != expand.Indexed || !spec.exported || !spec.readOnly {
		t.Fatalf("spec = %#v", spec)
	}
	if ended, err := applyDeclarationOption(spec, "--"); err != nil || !ended {
		t.Fatalf("applyDeclarationOption(--) = %t, %v", ended, err)
	}
	if _, err := applyDeclarationOption(spec, "-z"); err == nil {
		t.Fatal("unsupported option was accepted")
	}
}

func TestDeclarationAssignmentParsing(t *testing.T) {
	assignment, err := parseDeclarationAssignment("value+=suffix")
	if err != nil {
		t.Fatal(err)
	}
	if assignment.Name == nil || assignment.Name.Value != "value" || !assignment.Append || assignment.Naked || assignment.Value == nil || assignment.Value.Lit() != "suffix" {
		t.Fatalf("assignment = %#v", assignment)
	}
	if _, err := parseDeclarationAssignment("bad-name=value"); err == nil {
		t.Fatal("invalid variable name was accepted")
	}
}
