package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseDoesNotApplyResourceLimits(t *testing.T) {
	t.Run("source bytes", func(t *testing.T) {
		if _, err := Parse(context.Background(), strings.Repeat(" ", (1<<20)+1), "large.sh"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("AST nodes", func(t *testing.T) {
		const statements = 50_000
		file, err := Parse(context.Background(), strings.Repeat(":;", statements), "nodes.sh")
		if err != nil {
			t.Fatal(err)
		}
		if len(file.Stmts) != statements {
			t.Fatalf("statement count = %d, want %d", len(file.Stmts), statements)
		}
	})

	t.Run("arithmetic bytes", func(t *testing.T) {
		if _, err := ParseArithmetic(context.Background(), strings.Repeat("1", (1<<20)+1)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestParseHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Parse(ctx, "", "canceled.sh"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Parse() error = %v, want context.Canceled", err)
	}
	if _, err := ParseArithmetic(ctx, "1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ParseArithmetic() error = %v, want context.Canceled", err)
	}
}
