package runtime

import (
	"context"
	"io"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Parse reads Bash source while honoring context cancellation.
func Parse(ctx context.Context, source, name string) (*syntax.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(&contextReader{
		ctx:    ctx,
		reader: strings.NewReader(source),
	}, name)
	if err != nil {
		return nil, err
	}
	return file, nil
}

// ParseArithmetic parses a dynamically generated arithmetic expression.
func ParseArithmetic(ctx context.Context, source string) (syntax.ArithmExpr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	expression, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Arithmetic(&contextReader{
		ctx:    ctx,
		reader: strings.NewReader(source),
	})
	if err != nil {
		return nil, err
	}
	return expression, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
