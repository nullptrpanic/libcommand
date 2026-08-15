package materialize

import (
	"math"
	"strings"
	"testing"
)

func TestAddRejectsMaterializationOverflow(t *testing.T) {
	tests := []struct {
		name     string
		total    int
		addition int
		maximum  int
		want     int
		ok       bool
	}{
		{name: "below", total: 2, addition: 3, maximum: 8, want: 5, ok: true},
		{name: "exact", total: 3, addition: 5, maximum: 8, want: 8, ok: true},
		{name: "over", total: 4, addition: 5, maximum: 8},
		{name: "integer overflow", total: math.MaxInt, addition: 1, maximum: math.MaxInt},
		{name: "negative addition", addition: -1, maximum: 8},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := Add(test.total, test.addition, test.maximum)
			if got != test.want || ok != test.ok {
				t.Fatalf("Add(%d, %d, %d) = %d, %t; want %d, %t", test.total, test.addition, test.maximum, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestLimitErrorNamesMaximum(t *testing.T) {
	if err := LimitError(32); err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 32 reached") {
		t.Fatalf("LimitError(32) = %v", err)
	}
}
