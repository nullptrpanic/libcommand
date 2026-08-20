package analysis

import (
	"errors"
	"fmt"
)

// ErrRiskDetected identifies a command rejected by an analysis command.
var ErrRiskDetected = errors.New("command risk detected")

// DetectionError describes one high-confidence command risk.
type DetectionError struct {
	Command string
	Reason  string
}

func (e *DetectionError) Error() string {
	return fmt.Sprintf("%s: %q: %s", ErrRiskDetected, e.Command, e.Reason)
}

// Unwrap allows callers to classify DetectionError with errors.Is.
func (e *DetectionError) Unwrap() error {
	return ErrRiskDetected
}
