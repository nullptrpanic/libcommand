package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nullptrpanic/libcommand"
	shellruntime "github.com/nullptrpanic/libcommand/internal/runtime"
)

const (
	maximumTraceEvents            = 20_000
	maximumTraceBytes             = 4 << 20
	maximumPlaygroundOutputBytes  = 4 << 20
	maximumPlaygroundRequestBytes = 4 << 20
	maximumPlaygroundCommands     = 256
	maximumPlaygroundSteps        = 100_000
	maximumPlaygroundMemoryBytes  = 64 << 20
)

type playgroundRequest struct {
	Source            string               `json:"source"`
	Commands          []*playgroundCommand `json:"commands"`
	Env               map[string]string    `json:"env"`
	Args              []string             `json:"args"`
	Stdin             string               `json:"stdin"`
	MaxExecutionSteps int                  `json:"maxExecutionSteps"`
	MaxMemoryBytes    int                  `json:"maxMemoryBytes"`
}

type playgroundParseRequest struct {
	Source string `json:"source"`
}

type playgroundCommand struct {
	Name       string  `json:"name"`
	Stdout     string  `json:"stdout"`
	Stderr     string  `json:"stderr"`
	ExitCode   int     `json:"exitCode"`
	Error      string  `json:"error"`
	JavaScript *string `json:"javascript,omitempty"`
}

type playgroundInvocation struct {
	Sequence   uint64                         `json:"sequence"`
	NodeID     uint64                         `json:"nodeId"`
	PathID     uint64                         `json:"pathId"`
	Invocation *libcommand.Invocation         `json:"invocation"`
	Result     *libcommand.TraceCommandResult `json:"result,omitempty"`
}

type playgroundResponse struct {
	Nodes            []*libcommand.TraceNode  `json:"nodes"`
	ASTNodeCount     int                      `json:"astNodeCount"`
	Events           []*libcommand.TraceEvent `json:"events"`
	Invocations      []*playgroundInvocation  `json:"invocations"`
	Outputs          []*playgroundPathOutput  `json:"outputs"`
	DurationMicros   int64                    `json:"durationMicros"`
	PeakLogicalBytes int                      `json:"peakLogicalBytes"`
	Truncated        bool                     `json:"truncated"`
	Error            string                   `json:"error,omitempty"`
}

type playgroundPathOutput struct {
	PathID    uint64                      `json:"pathId"`
	Status    libcommand.TracePathStatus  `json:"status"`
	Result    *libcommand.TracePathResult `json:"result"`
	Truncated bool                        `json:"truncated,omitempty"`
}

func analyzeJSON(encoded string) string {
	return analyzeJSONWithTrace(encoded, nil)
}

func parseSourceJSON(encoded string) string {
	if len(encoded) > maximumPlaygroundRequestBytes {
		return encodePlaygroundResponse(&playgroundResponse{
			Error: fmt.Sprintf("playground request exceeds %d bytes", maximumPlaygroundRequestBytes),
		})
	}
	request := &playgroundParseRequest{}
	if err := json.Unmarshal([]byte(encoded), request); err != nil {
		return encodePlaygroundResponse(&playgroundResponse{Error: fmt.Sprintf("decode request: %v", err)})
	}
	nodes, err := shellruntime.ParseTraceNodes(context.Background(), request.Source, "command.sh")
	if err != nil {
		return encodePlaygroundResponse(&playgroundResponse{Error: fmt.Sprintf("parse bash: %v", err)})
	}
	return encodePlaygroundResponse(&playgroundResponse{Nodes: nodes, ASTNodeCount: len(nodes)})
}

func analyzeJSONWithTrace(encoded string, stream func(*libcommand.TraceEvent)) string {
	if len(encoded) > maximumPlaygroundRequestBytes {
		return encodePlaygroundResponse(&playgroundResponse{
			Error: fmt.Sprintf("playground request exceeds %d bytes", maximumPlaygroundRequestBytes),
		})
	}
	request := &playgroundRequest{}
	if err := json.Unmarshal([]byte(encoded), request); err != nil {
		return encodePlaygroundResponse(&playgroundResponse{Error: fmt.Sprintf("decode request: %v", err)})
	}
	if err := validatePlaygroundRequest(request); err != nil {
		return encodePlaygroundResponse(&playgroundResponse{Error: err.Error()})
	}
	return encodePlaygroundResponse(analyzeWithTrace(request, maximumTraceEvents, stream))
}

func validatePlaygroundRequest(request *playgroundRequest) error {
	if len(request.Commands) > maximumPlaygroundCommands {
		return fmt.Errorf("observed commands exceed %d entries", maximumPlaygroundCommands)
	}
	if request.MaxExecutionSteps < 0 || request.MaxExecutionSteps > maximumPlaygroundSteps {
		return fmt.Errorf("max execution steps must be between 0 and %d", maximumPlaygroundSteps)
	}
	if request.MaxMemoryBytes < 0 || request.MaxMemoryBytes > maximumPlaygroundMemoryBytes {
		return fmt.Errorf("max memory bytes must be between 0 and %d", maximumPlaygroundMemoryBytes)
	}
	for _, command := range request.Commands {
		if command == nil || strings.TrimSpace(command.Name) == "" {
			return fmt.Errorf("observed command name is required")
		}
		if command.JavaScript != nil {
			if strings.TrimSpace(*command.JavaScript) == "" {
				return fmt.Errorf("JavaScript handler source for %q is required", command.Name)
			}
			if command.Error != "" || command.Stdout != "" || command.Stderr != "" || command.ExitCode != 0 {
				return fmt.Errorf("JavaScript command %q cannot define a fixed result or error", command.Name)
			}
		}
		if command.Error != "" && (command.Stdout != "" || command.Stderr != "" || command.ExitCode != 0) {
			return fmt.Errorf("error command %q cannot define a result", command.Name)
		}
	}
	return nil
}

func analyze(request *playgroundRequest, maximumEvents int) *playgroundResponse {
	return analyzeWithTrace(request, maximumEvents, nil)
}

func analyzeWithTrace(request *playgroundRequest, maximumEvents int, stream func(*libcommand.TraceEvent)) *playgroundResponse {
	started := time.Now()
	response := &playgroundResponse{}
	wildcardConfigured := false
	builder := libcommand.NewBuilder().Limits(&libcommand.Limits{
		MaxExecutionSteps: request.MaxExecutionSteps,
		MaxMemoryBytes:    request.MaxMemoryBytes,
	})
	for _, command := range request.Commands {
		current := command
		wildcardConfigured = wildcardConfigured || current.Name == "*"
		builder.Command(current.Name, func(_ context.Context, _ *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
			if current.JavaScript != nil {
				return executeJavaScriptCommand(current.Name, invocation)
			}
			if current.Error != "" {
				return nil, errors.New(current.Error)
			}
			return &libcommand.CommandResult{
				Stdout:   []byte(current.Stdout),
				Stderr:   []byte(current.Stderr),
				ExitCode: current.ExitCode,
			}, nil
		})
	}
	if !wildcardConfigured {
		builder.Command("*", func(context.Context, *libcommand.CommandContext, *libcommand.Invocation) (*libcommand.CommandResult, error) {
			return &libcommand.CommandResult{Unresolved: true}, nil
		})
	}

	collected := 0
	collectedBytes := 0
	outputBytes := 0
	traceTruncated := false
	collectingASTNodes := true
	observer := func(event *libcommand.TraceEvent) bool {
		recordPlaygroundOutput(response, event, &outputBytes)
		if event.SnapshotTruncated {
			response.Truncated = true
		}
		if event.CommandResult != nil && event.CommandResult.OutputTruncated {
			response.Truncated = true
		}
		if traceTruncated {
			return true
		}
		displayEvent := event
		if event.PathResult != nil {
			cloned := *event
			cloned.PathResult = nil
			displayEvent = &cloned
		}
		eventBytes := traceDisplayBytes(displayEvent)
		if collected >= maximumEvents || collectedBytes+eventBytes > maximumTraceBytes {
			response.Truncated = true
			traceTruncated = true
			return true
		}
		collected++
		collectedBytes += eventBytes
		if event.Kind == libcommand.TraceNodeDiscovered {
			response.Nodes = append(response.Nodes, event.Node)
			if collectingASTNodes {
				response.ASTNodeCount++
			}
		} else {
			response.Events = append(response.Events, displayEvent)
			if event.Kind != libcommand.TraceSimulationStarted {
				collectingASTNodes = false
			}
		}
		if stream != nil {
			stream(displayEvent)
		}
		if event.Memory != nil && event.Memory.AggregateBytes > response.PeakLogicalBytes {
			response.PeakLogicalBytes = event.Memory.AggregateBytes
		}
		recordPlaygroundInvocation(response, event)
		return true
	}

	simulator := builder.Build()
	err := simulator.SimulateTraceWithOptions(context.Background(), &libcommand.SimulationRequest{
		Source: request.Source,
		Env:    request.Env,
		Args:   request.Args,
		Stdin:  []byte(request.Stdin),
	}, &libcommand.TraceOptions{
		StateSnapshots:   true,
		MaxSnapshotBytes: maximumTraceBytes,
	}, observer)
	response.DurationMicros = time.Since(started).Microseconds()
	if err != nil {
		response.Error = err.Error()
	}
	return response
}

func recordPlaygroundOutput(response *playgroundResponse, event *libcommand.TraceEvent, retainedBytes *int) {
	if event.Kind != libcommand.TracePathCompleted || event.PathResult == nil {
		return
	}
	result := *event.PathResult
	remaining := max(0, maximumPlaygroundOutputBytes-*retainedBytes)
	var stdoutTruncated, stderrTruncated, errorTruncated bool
	result.Stdout, remaining, stdoutTruncated = retainOutput(result.Stdout, remaining)
	result.Stderr, remaining, stderrTruncated = retainOutput(result.Stderr, remaining)
	result.Error, remaining, errorTruncated = retainOutput(result.Error, remaining)
	*retainedBytes = maximumPlaygroundOutputBytes - remaining
	truncated := stdoutTruncated || stderrTruncated || errorTruncated
	if truncated {
		response.Truncated = true
	}
	response.Outputs = append(response.Outputs, &playgroundPathOutput{
		PathID:    event.PathID,
		Status:    event.Status,
		Result:    &result,
		Truncated: truncated,
	})
}

func retainOutput(value string, remaining int) (string, int, bool) {
	if len(value) <= remaining {
		return value, remaining - len(value), false
	}
	return value[:remaining], 0, true
}

func traceDisplayBytes(event *libcommand.TraceEvent) int {
	const objectOverhead = 256
	size := objectOverhead + len(event.ChildPathIDs)*24 + 6*len(event.Error)
	if event.Node != nil {
		size += objectOverhead + 6*(len(event.Node.Kind)+len(event.Node.Snippet)+len(event.Node.FlowCommand)+len(event.Node.FlowFunction)+len(event.Node.FlowGroupExit))
		if event.Node.Source != nil {
			size += 6 * len(event.Node.Source.Name)
		}
	}
	if event.Snapshot != nil {
		snapshot := event.Snapshot
		size += objectOverhead + 6*(len(snapshot.Directory)+len(snapshot.Stdin)+len(snapshot.Stdout)+len(snapshot.Stderr)+len(snapshot.Error))
		for _, argument := range snapshot.Args {
			size += 16 + 6*len(argument)
		}
		for _, variable := range snapshot.Variables {
			size += 64 + 6*(len(variable.Name)+len(variable.Kind)+len(variable.Value))
		}
	}
	if event.CommandResult != nil {
		result := event.CommandResult
		size += objectOverhead + 6*(len(result.Stdout)+len(result.Stderr))
	}
	if event.Invocation == nil {
		return size
	}
	size += objectOverhead + 6*(len(event.Invocation.Name)+len(event.Invocation.Dir)+len(event.Invocation.Stdin))
	for _, argument := range event.Invocation.Args {
		size += 32 + 6*len(argument.Value)
	}
	for name, value := range event.Invocation.Env {
		size += 16 + 6*(len(name)+len(value))
	}
	if event.Invocation.Unresolved != nil {
		for _, name := range event.Invocation.Unresolved.Env {
			size += 8 + 6*len(name)
		}
	}
	// Observed command invocations are also retained in the dedicated list.
	return size * 2
}

func recordPlaygroundInvocation(response *playgroundResponse, event *libcommand.TraceEvent) {
	if event.Kind == libcommand.TraceCommandStarted && event.Invocation != nil {
		response.Invocations = append(response.Invocations, &playgroundInvocation{
			Sequence:   event.Sequence,
			NodeID:     event.NodeID,
			PathID:     event.PathID,
			Invocation: event.Invocation,
		})
		return
	}
	if event.Kind != libcommand.TraceCommandFinished {
		return
	}
	for index := len(response.Invocations) - 1; index >= 0; index-- {
		invocation := response.Invocations[index]
		if invocation.Result == nil && invocation.NodeID == event.NodeID && invocation.PathID == event.PathID {
			invocation.Result = event.CommandResult
			return
		}
	}
}

func encodePlaygroundResponse(response *playgroundResponse) string {
	encoded, err := json.Marshal(response)
	if err != nil {
		return `{"error":"encode response"}`
	}
	return string(encoded)
}
