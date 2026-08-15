package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestParseTraceNodesReturnsStaticStatementHierarchy(t *testing.T) {
	const source = "echo before\nif true; then\n  echo inside\nfi"
	nodes, err := ParseTraceNodes(context.Background(), source, "preview.sh")
	if err != nil {
		t.Fatal(err)
	}

	type expectedNode struct {
		id       uint64
		parentID uint64
		kind     string
		snippet  string
		line     uint
		embedded bool
	}
	want := []*expectedNode{
		{id: 1, kind: "command", snippet: "echo before", line: 1},
		{id: 2, kind: "condition", snippet: "if true; then echo inside fi", line: 2},
		{id: 3, parentID: 2, kind: "command", snippet: "true;", line: 2, embedded: true},
		{id: 4, parentID: 2, kind: "command", snippet: "echo inside", line: 3},
	}
	if len(nodes) != len(want) {
		t.Fatalf("node count = %d, want %d: %#v", len(nodes), len(want), nodes)
	}
	for index, expected := range want {
		node := nodes[index]
		if node.ID != expected.id || node.ParentID != expected.parentID || node.Kind != expected.kind || node.Snippet != expected.snippet || node.Embedded != expected.embedded {
			t.Fatalf("node %d = %#v, want %#v", index, node, expected)
		}
		if node.Source == nil || node.Source.Name != "preview.sh" || node.Source.Line != expected.line {
			t.Fatalf("node %d source = %#v, want preview.sh line %d", index, node.Source, expected.line)
		}
	}
}

func TestParseTraceNodesMarksInternalEvaluationStatementsEmbedded(t *testing.T) {
	const source = `if [[ -n "$CHAT_ID" ]]; then
  echo reached
fi
for i in $(seq 1 2); do
  if (( i == 1 )); then
    text=first
  else
    text=other
  fi
  lark-cli send "$text"
done
printf done | base64`

	nodes, err := ParseTraceNodes(context.Background(), source, "preview.sh")
	if err != nil {
		t.Fatal(err)
	}

	wantEmbedded := map[string]bool{
		`[[ -n "$CHAT_ID" ]];`: true,
		"seq 1 2":              true,
		"(( i == 1 ));":        true,
		"printf done":          true,
		"base64":               true,
	}
	wantFlowGroups := map[string]uint32{
		"text=first": 0,
		"text=other": 1,
	}
	wantFlowCanSkip := map[string]bool{
		`if [[ -n "$CHAT_ID" ]]; then echo reached fi`:                                                           true,
		`for i in $(seq 1 2); do if (( i == 1 )); then text=first else text=other fi lark-cli send "$text" done`: true,
		`if (( i == 1 )); then text=first else text=other fi`:                                                    false,
	}
	for _, node := range nodes {
		want := wantEmbedded[node.Snippet]
		if node.Embedded != want {
			t.Errorf("node %q embedded = %v, want %v", node.Snippet, node.Embedded, want)
		}
		if group, relevant := wantFlowGroups[node.Snippet]; relevant && node.FlowGroup != group {
			t.Errorf("node %q flow group = %d, want %d", node.Snippet, node.FlowGroup, group)
		}
		if canSkip, relevant := wantFlowCanSkip[node.Snippet]; relevant && node.FlowCanSkip != canSkip {
			t.Errorf("node %q flow can skip = %v, want %v", node.Snippet, node.FlowCanSkip, canSkip)
		}
	}

	for snippet := range wantEmbedded {
		found := false
		for _, node := range nodes {
			if node.Snippet == snippet {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing expected embedded node %q", snippet)
		}
	}
}

func TestParseTraceNodesDescribesControlFlowSemantics(t *testing.T) {
	const source = `f() { echo body; }
for item in; do
  break
done
case "$item" in
  first) echo first ;&
  *) echo fallback ;;
esac
f`

	nodes, err := ParseTraceNodes(context.Background(), source, "preview.sh")
	if err != nil {
		t.Fatal(err)
	}

	bySnippet := make(map[string]*TraceNode, len(nodes))
	for _, node := range nodes {
		bySnippet[node.Snippet] = node
	}
	if got := bySnippet[`f() { echo body; }`].FlowFunction; got != "f" {
		t.Fatalf("function name = %q, want f", got)
	}
	if got := bySnippet["f"].FlowCommand; got != "f" {
		t.Fatalf("call name = %q, want f", got)
	}
	if got := bySnippet["break"].FlowCommand; got != "break" {
		t.Fatalf("control command = %q, want break", got)
	}
	if !bySnippet["for item in; do break done"].FlowCanSkip {
		t.Fatal("loop must expose its zero-iteration exit")
	}
	if got := bySnippet["echo first"].FlowGroupExit; got != ";&" {
		t.Fatalf("first case exit = %q, want ;&", got)
	}
	fallback := bySnippet["echo fallback"]
	if got := fallback.FlowGroupExit; got != ";;" {
		t.Fatalf("fallback case exit = %q, want ;;", got)
	}
	if !fallback.FlowGroupDefault {
		t.Fatal("literal * case item must be marked as the default group")
	}
	if bySnippet[`case "$item" in first) echo first ;& *) echo fallback ;; esac`].FlowCanSkip {
		t.Fatal("case with a literal * item must not expose a no-match exit")
	}
}

func TestParseTraceNodesDistinguishesUnconditionalLoopAndLiteralCasePattern(t *testing.T) {
	const source = `for ((;;)); do
  echo forever
done
case "$item" in
  \*) echo literal-star ;;
esac`

	nodes, err := ParseTraceNodes(context.Background(), source, "preview.sh")
	if err != nil {
		t.Fatal(err)
	}

	var loopNode, caseNode, literalNode *TraceNode
	for _, node := range nodes {
		switch {
		case node.Kind == "loop":
			loopNode = node
		case strings.HasPrefix(node.Snippet, "case "):
			caseNode = node
		case node.Snippet == "echo literal-star":
			literalNode = node
		}
	}
	if loopNode == nil || caseNode == nil || literalNode == nil {
		t.Fatalf("trace nodes = %#v, want loop, case, and literal-pattern body", nodes)
	}
	if loopNode.FlowCanSkip {
		t.Fatal("C-style loop without a condition must not expose a condition exit")
	}
	if !caseNode.FlowCanSkip {
		t.Fatal("case with an escaped literal star must retain its no-match exit")
	}
	if literalNode.FlowGroupDefault {
		t.Fatal("escaped literal star must not be marked as a default case group")
	}
}
