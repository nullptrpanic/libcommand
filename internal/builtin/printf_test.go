package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func TestPrintfWidthLimitCountsCumulativeWidths(t *testing.T) {
	if printfWidthWithinLimit("%100s%100s", 128) {
		t.Fatal("two cumulative widths above the limit were accepted")
	}
	if !printfWidthWithinLimit("%64s%64s", 128) {
		t.Fatal("cumulative widths at the limit were rejected")
	}
	if printfWidthWithinLimit("%80c%80b", 128) {
		t.Fatal("character and escape widths above the limit were accepted")
	}
	if !printfWidthWithinLimit("%64c%64b", 128) {
		t.Fatal("character and escape widths at the limit were rejected")
	}
}

func TestPreparePrintfFormatDoesNotAllocatePerFlag(t *testing.T) {
	format := "%" + strings.Repeat("+", 256) + "*s"
	allocations := float64(0)
	err := executeBuiltinScript(t, "measure", &runtime.Request{}, map[string]*runtime.CommandDefinition{
		"measure": {
			Command: func(_ context.Context, command *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
				allocations = testing.AllocsPerRun(5, func() {
					_, _, _, _, _, _ = preparePrintfFormat(command, format, []string{"1", "value"})
				})
				return command.Result(command.Output().Build()), nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if allocations > 32 {
		t.Fatalf("allocations = %.0f, want at most 32", allocations)
	}
}
