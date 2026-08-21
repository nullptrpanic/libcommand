package analysis

import (
	"errors"
	"fmt"
)

// ErrRiskDetected identifies a command rejected by an analysis command.
var ErrRiskDetected = errors.New("command risk detected")

// RiskType identifies one stable category of high-confidence command risk.
type RiskType string

const (
	RiskTypeReverseShell                   RiskType = "reverse_shell"
	RiskTypeDestructiveOperation           RiskType = "destructive_operation"
	RiskTypeSensitiveInformationDisclosure RiskType = "sensitive_information_disclosure"
	RiskTypeDataExfiltration               RiskType = "data_exfiltration"
)

// DetectionError describes one high-confidence command risk.
type DetectionError struct {
	Command string
	Type    RiskType
}

func (e *DetectionError) Error() string {
	return fmt.Sprintf("%s: %q: %s", ErrRiskDetected, e.Command, e.Type)
}

// Unwrap allows callers to classify DetectionError with errors.Is.
func (e *DetectionError) Unwrap() error {
	return ErrRiskDetected
}
