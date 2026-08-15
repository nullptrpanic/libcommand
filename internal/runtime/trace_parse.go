package runtime

import "context"

// ParseTraceNodes parses source and returns the static statement skeleton used
// by execution traces without evaluating the Shell program.
func ParseTraceNodes(ctx context.Context, source, name string) ([]*TraceNode, error) {
	file, err := Parse(ctx, source, name)
	if err != nil {
		return nil, err
	}

	nodes := make([]*TraceNode, 0, len(file.Stmts))
	trace := newExecutionTrace(func(event *TraceEvent) bool {
		if event.Kind == TraceNodeDiscovered && event.Node != nil {
			nodes = append(nodes, event.Node)
		}
		return true
	}, nil)
	trace.discover(file, source, name, 0)
	return nodes, nil
}
