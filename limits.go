package libcommand

import (
	"fmt"
	"path"
	"sort"

	"github.com/nullptrpanic/libcommand/internal/materialize"
)

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
	total := 0
	ok := true
	add := func(sizes ...int) {
		for _, size := range sizes {
			if !ok {
				return
			}
			total, ok = materialize.Add(total, size, maximum)
		}
	}
	add(len(request.Source), len(request.Stdin), len(request.WorkingDir), len(request.User))
	for _, argument := range request.Args {
		if !ok {
			break
		}
		add(materialize.EntryBytes, len(argument))
	}
	for name, value := range request.Env {
		if !ok {
			break
		}
		add(materialize.EntryBytes, len(name), len(value))
	}
	for name, contents := range request.Files {
		if !ok {
			break
		}
		add(materialize.EntryBytes, len(name), len(contents))
	}
	if !ok {
		return materialize.LimitError(maximum)
	}
	return nil
}

func normalizeInitialFiles(request *SimulationRequest) (string, map[string][]byte, error) {
	workingDir := request.WorkingDir
	if workingDir == "" {
		workingDir = "/"
	} else if !path.IsAbs(workingDir) {
		workingDir = path.Join("/", workingDir)
	} else {
		workingDir = path.Clean(workingDir)
	}

	names := make([]string, 0, len(request.Files))
	for name := range request.Files {
		names = append(names, name)
	}
	sort.Strings(names)

	files := make(map[string][]byte, len(names))
	originalNames := make(map[string]string, len(names))
	for _, name := range names {
		if name == "" {
			return "", nil, fmt.Errorf("initial file path is empty")
		}
		resolved := name
		if !path.IsAbs(resolved) {
			resolved = path.Join(workingDir, resolved)
		} else {
			resolved = path.Clean(resolved)
		}
		if previous, exists := originalNames[resolved]; exists {
			return "", nil, fmt.Errorf("initial files %q and %q resolve to the same virtual path %q", previous, name, resolved)
		}
		files[resolved] = request.Files[name]
		originalNames[resolved] = name
	}

	return workingDir, files, nil
}
