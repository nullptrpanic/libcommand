// Package materialize provides overflow-safe accounting for logical data
// materialized by one simulation operation.
package materialize

import "fmt"

const (
	DefaultMaxBytes = 2 << 20
	EntryBytes      = 16
)

// Add includes addition in total when the result fits within maximum.
func Add(total, addition, maximum int) (int, bool) {
	if total < 0 || addition < 0 || maximum < 0 || total > maximum || addition > maximum-total {
		return 0, false
	}
	return total + addition, true
}

// LimitError reports the configured logical materialization ceiling.
func LimitError(maximum int) error {
	return fmt.Errorf("maximum materialized byte count %d reached", maximum)
}
