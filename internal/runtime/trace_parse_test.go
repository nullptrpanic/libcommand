package runtime

import (
	"context"
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
	for _, node := range nodes {
		want := wantEmbedded[node.Snippet]
		if node.Embedded != want {
			t.Errorf("node %q embedded = %v, want %v", node.Snippet, node.Embedded, want)
		}
		if group, relevant := wantFlowGroups[node.Snippet]; relevant && node.FlowGroup != group {
			t.Errorf("node %q flow group = %d, want %d", node.Snippet, node.FlowGroup, group)
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
