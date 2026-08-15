package libcommand

import "github.com/nullptrpanic/libcommand/internal/materialize"

const (
	defaultMaxExecutionSteps = 10_000
	defaultMaxMemoryBytes    = materialize.DefaultMaxBytes
)

// Limits controls resource budgets for one simulation. Zero values use the
// package defaults; nonzero values must be positive.
type Limits struct {
	MaxExecutionSteps int
	MaxMemoryBytes    int
}

func limitsWithDefaults(limits *Limits) *Limits {
	result := &Limits{}
	if limits != nil {
		*result = *limits
	}
	if result.MaxExecutionSteps == 0 {
		result.MaxExecutionSteps = defaultMaxExecutionSteps
	}
	if result.MaxMemoryBytes == 0 {
		result.MaxMemoryBytes = defaultMaxMemoryBytes
	}
	return result
}

// checkUntrustedRequestMaterialization bounds request data before the
// evaluator copies or expands it.
func checkUntrustedRequestMaterialization(request *SimulationRequest, maximum int) error {
	total, ok := materialize.Add(0, len(request.Source), maximum)
	if ok {
		total, ok = materialize.Add(total, len(request.Stdin), maximum)
	}
	for _, argument := range request.Args {
		if !ok {
			break
		}
		total, ok = materialize.Add(total, materialize.EntryBytes, maximum)
		if ok {
			total, ok = materialize.Add(total, len(argument), maximum)
		}
	}
	for name, value := range request.Env {
		if !ok {
			break
		}
		total, ok = materialize.Add(total, materialize.EntryBytes, maximum)
		if ok {
			total, ok = materialize.Add(total, len(name), maximum)
		}
		if ok {
			total, ok = materialize.Add(total, len(value), maximum)
		}
	}
	if !ok {
		return materialize.LimitError(maximum)
	}
	return nil
}
