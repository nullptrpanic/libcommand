package runtime

import (
	"context"
	"errors"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

func TestCandidateIndexMarksRegisteredCommandAncestors(t *testing.T) {
	file := parseForTest(t, `for i in "$(unknown)"; do lark-cli "$i" >output; done`, "candidate.sh")
	index, err := buildCandidateIndex(context.Background(), file, func(name string) bool { return name == "lark-cli" })
	if err != nil {
		t.Fatal(err)
	}
	clause := file.Stmts[0].Cmd.(*syntax.ForClause)
	if !index.contains(file) || !index.contains(file.Stmts[0]) || !index.contains(clause) || !index.contains(clause.Do[0]) {
		t.Fatalf("candidate ancestors were not indexed")
	}
}

func TestCandidateIndexMarksDynamicCommandAncestors(t *testing.T) {
	file := parseForTest(t, `for i in "$(unknown)"; do "$command" "$i"; done`, "dynamic-candidate.sh")
	index, err := buildCandidateIndex(context.Background(), file, func(name string) bool { return name == "lark-cli" })
	if err != nil {
		t.Fatal(err)
	}
	clause := file.Stmts[0].Cmd.(*syntax.ForClause)
	if !index.contains(file) || !index.contains(file.Stmts[0]) || !index.contains(clause) || !index.contains(clause.Do[0]) {
		t.Fatalf("dynamic candidate ancestors were not indexed")
	}
}

func TestCandidateIndexIgnoresNonExecutableText(t *testing.T) {
	file := parseForTest(t, "cat <<'EOF'\nlark-cli hidden\nEOF\n", "heredoc.sh")
	index, err := buildCandidateIndex(context.Background(), file, func(name string) bool { return name == "lark-cli" })
	if err != nil {
		t.Fatal(err)
	}
	if index.contains(file) || index.contains(file.Stmts[0]) {
		t.Fatal("here-document text was indexed as an executable command")
	}
}

func TestCandidateIndexHonorsContextCancellation(t *testing.T) {
	file := parseForTest(t, `lark-cli`, "canceled-candidate.sh")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := buildCandidateIndex(ctx, file, func(name string) bool { return name == "lark-cli" }); !errors.Is(err, context.Canceled) {
		t.Fatalf("buildCandidateIndex() error = %v, want context.Canceled", err)
	}
}
