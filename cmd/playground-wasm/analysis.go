package main

import (
	"context"
	"errors"

	"github.com/nullptrpanic/libcommand"
	commandanalysis "github.com/nullptrpanic/libcommand/analysis"
)

type playgroundDetection struct {
	Sequence uint64                   `json:"sequence"`
	NodeID   uint64                   `json:"nodeId"`
	PathID   uint64                   `json:"pathId"`
	Command  string                   `json:"command"`
	Type     commandanalysis.RiskType `json:"type"`
	Error    string                   `json:"error"`
}

type playgroundCommandLocation struct {
	Sequence uint64
	NodeID   uint64
	PathID   uint64
}

type playgroundAnalyzer struct {
	active     map[uint64][]*playgroundCommandLocation
	current    *playgroundCommandLocation
	detections []*playgroundDetection
	stream     func(*playgroundStreamItem)
}

func newPlaygroundAnalyzer(stream func(*playgroundStreamItem)) *playgroundAnalyzer {
	return &playgroundAnalyzer{
		active: make(map[uint64][]*playgroundCommandLocation),
		stream: stream,
	}
}

func (a *playgroundAnalyzer) middleware(next libcommand.Command) libcommand.Command {
	return func(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
		a.detect(ctx, shell, invocation)
		return next(ctx, shell, invocation)
	}
}

func (a *playgroundAnalyzer) detect(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) {
	handler := lookupPlaygroundAnalysisCommand(invocation.Name)
	err := commandanalysis.Inspect(ctx, shell, invocation, handler)
	detectionError := new(commandanalysis.DetectionError)
	if !errors.As(err, &detectionError) {
		return
	}
	a.recordDetection(&playgroundDetection{
		Command: invocation.Name,
		Type:    detectionError.Type,
		Error:   err.Error(),
	}, a.current)
}

func (a *playgroundAnalyzer) recordDetection(detection *playgroundDetection, location *playgroundCommandLocation) {
	if location != nil {
		detection.Sequence = location.Sequence
		detection.NodeID = location.NodeID
		detection.PathID = location.PathID
	}
	for _, existing := range a.detections {
		if existing.Sequence == detection.Sequence && existing.Type == detection.Type {
			return
		}
	}
	a.detections = append(a.detections, detection)
	if a.stream != nil {
		a.stream(&playgroundStreamItem{Type: "detection", Detection: detection})
	}
}

func (a *playgroundAnalyzer) observe(event *libcommand.TraceEvent) {
	switch event.Kind {
	case libcommand.TraceCommandStarted:
		location := &playgroundCommandLocation{
			Sequence: event.Sequence,
			NodeID:   event.NodeID,
			PathID:   event.PathID,
		}
		a.active[event.PathID] = append(a.active[event.PathID], location)
		a.current = location
	case libcommand.TraceCommandFinished:
		stack := a.active[event.PathID]
		if len(stack) == 0 {
			return
		}
		stack = stack[:len(stack)-1]
		a.active[event.PathID] = stack
		a.current = nil
		if len(stack) != 0 {
			a.current = stack[len(stack)-1]
		}
	}
}
