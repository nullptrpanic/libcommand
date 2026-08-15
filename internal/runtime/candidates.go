package runtime

import (
	"context"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/syntax"
)

func commandCandidateLookup(lookup CommandLookupFunc) func(string) bool {
	return func(name string) bool {
		if lookupLanguageControl(name) != nil {
			return false
		}
		if lookup == nil {
			return false
		}
		definition := lookup(name)
		if !commandDefinitionExecutable(definition) {
			return false
		}
		if definition.Fallback {
			return definition.UserOverride
		}
		return definition.Candidate
	}
}

type candidateIndex struct {
	nodes              map[syntax.Node]struct{}
	parents            map[syntax.Node]syntax.Node
	calls              map[string][]syntax.Node
	candidateFunctions map[string]struct{}
	dynamicBytes       int
}

func buildCandidateIndex(ctx context.Context, file *syntax.File, commandExists func(string) bool) (*candidateIndex, error) {
	index := &candidateIndex{
		nodes:              make(map[syntax.Node]struct{}),
		parents:            make(map[syntax.Node]syntax.Node),
		calls:              make(map[string][]syntax.Node),
		candidateFunctions: make(map[string]struct{}),
	}
	if err := index.add(ctx, file, commandExists); err != nil {
		return nil, err
	}
	return index, nil
}

func (index *candidateIndex) addDynamic(ctx context.Context, file *syntax.File, sourceBytes int, commandExists func(string) bool, maximum int) error {
	if index == nil || file == nil {
		return nil
	}
	additional, err := candidateIndexMaterialization(ctx, file, sourceBytes, maximum)
	if err != nil {
		return err
	}
	total, ok := materialize.Add(index.dynamicBytes, additional, maximum)
	if !ok {
		return materialize.LimitError(maximum)
	}
	if err := index.add(ctx, file, commandExists); err != nil {
		return err
	}
	index.dynamicBytes = total
	return nil
}

func candidateIndexMaterialization(ctx context.Context, file *syntax.File, sourceBytes, maximum int) (int, error) {
	total, ok := materialize.Add(0, sourceBytes, maximum)
	if !ok {
		return 0, materialize.LimitError(maximum)
	}
	var walkErr error
	syntax.Walk(file, func(node syntax.Node) bool {
		if node == nil {
			return true
		}
		if err := ctx.Err(); err != nil {
			walkErr = err
			return false
		}
		total, ok = materialize.Add(total, 2*materialize.EntryBytes, maximum)
		if !ok {
			walkErr = materialize.LimitError(maximum)
			return false
		}
		return true
	})
	if walkErr != nil {
		return 0, walkErr
	}
	return total, nil
}

func (index *candidateIndex) add(ctx context.Context, file *syntax.File, commandExists func(string) bool) error {
	if index == nil || file == nil || commandExists == nil {
		return nil
	}
	var ancestors []syntax.Node
	var candidates []syntax.Node
	var walkErr error
	syntax.Walk(file, func(node syntax.Node) bool {
		if node == nil {
			ancestors = ancestors[:len(ancestors)-1]
			return true
		}
		if err := ctx.Err(); err != nil {
			walkErr = err
			return false
		}
		if len(ancestors) != 0 {
			index.parents[node] = ancestors[len(ancestors)-1]
		}
		ancestors = append(ancestors, node)
		name := ""
		switch command := node.(type) {
		case *syntax.CallExpr:
			if len(command.Args) != 0 {
				name = command.Args[0].Lit()
			}
		case *syntax.DeclClause:
			if command.Variant != nil {
				name = command.Variant.Value
			}
		case *syntax.LetClause:
			name = "let"
		default:
			return true
		}
		if name != "" {
			index.calls[name] = append(index.calls[name], node)
		}
		_, candidateFunction := index.candidateFunctions[name]
		if name == "" || commandExists(name) || candidateFunction {
			candidates = append(candidates, node)
		}
		return true
	})
	if walkErr != nil {
		return walkErr
	}

	var functions []string
	for _, candidate := range candidates {
		functions = index.mark(candidate, functions)
	}
	for len(functions) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := functions[0]
		functions = functions[1:]
		for _, call := range index.calls[name] {
			functions = index.mark(call, functions)
		}
	}
	return ctx.Err()
}

func (index *candidateIndex) mark(node syntax.Node, functions []string) []string {
	for current := node; current != nil; current = index.parents[current] {
		if _, exists := index.nodes[current]; exists {
			break
		}
		index.nodes[current] = struct{}{}
		declaration, ok := current.(*syntax.FuncDecl)
		if !ok || declaration.Name == nil {
			continue
		}
		name := declaration.Name.Value
		if _, exists := index.candidateFunctions[name]; exists {
			continue
		}
		index.candidateFunctions[name] = struct{}{}
		functions = append(functions, name)
	}
	return functions
}

func (index *candidateIndex) contains(node syntax.Node) bool {
	if index == nil || node == nil {
		return false
	}
	_, exists := index.nodes[node]
	return exists
}
