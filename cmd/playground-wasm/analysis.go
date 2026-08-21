package main

import (
	"context"
	"errors"
	"path"
	"strings"

	"github.com/nullptrpanic/libcommand"
	commandanalysis "github.com/nullptrpanic/libcommand/analysis"
)

var playgroundAnalysisCommands = map[string]libcommand.Command{
	"rm":        commandanalysis.RM,
	"poweroff":  commandanalysis.Poweroff,
	"reboot":    commandanalysis.Reboot,
	"halt":      commandanalysis.Halt,
	"shutdown":  commandanalysis.Shutdown,
	"init":      commandanalysis.Init,
	"telinit":   commandanalysis.Telinit,
	"systemctl": commandanalysis.Systemctl,
	"nc":        commandanalysis.NC,
	"ncat":      commandanalysis.Ncat,
	"netcat":    commandanalysis.Netcat,
	"socat":     commandanalysis.Socat,
	"find":      commandanalysis.Find,
	"mkfs":      commandanalysis.Mkfs,
	"wipefs":    commandanalysis.Wipefs,
	"dd":        commandanalysis.DD,
	"sh":        commandanalysis.Shell,
	"bash":      commandanalysis.Shell,
	"dash":      commandanalysis.Shell,
	"ksh":       commandanalysis.Shell,
	"zsh":       commandanalysis.Shell,
	"python":    commandanalysis.Python,
	"python2":   commandanalysis.Python,
	"python3":   commandanalysis.Python,
	"perl":      commandanalysis.Perl,
}

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
}

func newPlaygroundAnalyzer() *playgroundAnalyzer {
	return &playgroundAnalyzer{active: make(map[uint64][]*playgroundCommandLocation)}
}

func (a *playgroundAnalyzer) middleware(next libcommand.Command) libcommand.Command {
	return func(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
		a.detect(ctx, shell, invocation)
		return next(ctx, shell, invocation)
	}
}

func (a *playgroundAnalyzer) detect(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) {
	handler := playgroundAnalysisCommands[invocation.Name]
	if handler == nil {
		base := path.Base(invocation.Name)
		handler = playgroundAnalysisCommands[base]
		if handler == nil && strings.HasPrefix(base, "mkfs.") {
			handler = commandanalysis.Mkfs
		}
		if handler == nil && strings.HasPrefix(base, "python") {
			handler = commandanalysis.Python
		}
	}
	if handler == nil {
		return
	}
	a.runDetector(ctx, shell, invocation, handler)
}

func (a *playgroundAnalyzer) runDetector(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation, handler libcommand.Command) bool {
	_, err := handler(ctx, shell, invocation)
	detectionError := new(commandanalysis.DetectionError)
	if !errors.As(err, &detectionError) {
		return false
	}
	detection := &playgroundDetection{
		Command: invocation.Name,
		Type:    detectionError.Type,
		Error:   err.Error(),
	}
	if a.current != nil {
		detection.Sequence = a.current.Sequence
		detection.NodeID = a.current.NodeID
		detection.PathID = a.current.PathID
	}
	a.detections = append(a.detections, detection)
	return true
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
