package analysis

import (
	"context"
	"path"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

const (
	maximumTrackedFIFOs    = 256
	maximumTrackedPathSize = 4 << 10
)

type streamStage uint8

const (
	streamNone streamStage = iota
	streamFIFO
	streamInteractiveShell
)

type streamFact struct {
	stage streamStage
	fifo  string
}

type pathFacts struct {
	fifos  *fifoFact
	stream streamFact
}

type fifoFact struct {
	name     string
	present  bool
	previous *fifoFact
	count    int
}

// Session correlates command facts across one simulation. Callers own command
// lookup and decide whether a returned DetectionError blocks execution.
type Session struct {
	paths map[uint64]*pathFacts
}

type observationContextKey struct{}
type sessionContextKey struct{}

type observation struct {
	facts   *pathFacts
	handled bool
}

// NewSession creates isolated analysis state for one simulation.
func NewSession() *Session {
	return &Session{paths: make(map[uint64]*pathFacts)}
}

// WithSession attaches request-local analysis state to a simulation context.
func WithSession(ctx context.Context, session *Session) context.Context {
	return context.WithValue(ctx, sessionContextKey{}, session)
}

// Inspect runs the caller-selected detector and updates request-local,
// path-local facts. A nil detector still breaks pending command flow. Without
// a Session in ctx, stateless detectors still run but no facts are retained.
func Inspect(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation, detector libcommand.Command) error {
	session, _ := ctx.Value(sessionContextKey{}).(*Session)
	if session == nil {
		if detector == nil {
			return nil
		}
		_, err := detector(ctx, shell, invocation)
		return err
	}
	return session.inspect(ctx, shell, invocation, detector)
}

func (s *Session) inspect(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation, detector libcommand.Command) error {
	current := &observation{facts: s.facts(shell.State())}
	ctx = context.WithValue(ctx, observationContextKey{}, current)
	if detector == nil {
		current.facts.stream = streamFact{}
		return nil
	}
	_, err := detector(ctx, shell, invocation)
	if !current.handled {
		current.facts.stream = streamFact{}
	}
	return err
}

func (s *Session) facts(state *libcommand.State) *pathFacts {
	pathID := state.PathID()
	if facts := s.paths[pathID]; facts != nil {
		return facts
	}
	facts := &pathFacts{}
	if parent := state.Parent(); parent != nil {
		facts = clonePathFacts(s.facts(parent))
	}
	s.paths[pathID] = facts
	return facts
}

func clonePathFacts(source *pathFacts) *pathFacts {
	return &pathFacts{fifos: source.fifos, stream: source.stream}
}

func currentObservation(ctx context.Context) *observation {
	current, _ := ctx.Value(observationContextKey{}).(*observation)
	return current
}

func observeMkfifo(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) {
	current := currentObservation(ctx)
	if current == nil {
		return
	}
	current.handled = true
	current.facts.stream = streamFact{}
	for _, name := range mkfifoOperands(invocation.Args) {
		if len(name) > maximumTrackedPathSize {
			continue
		}
		resolved, ok := resolvedPath(invocation.Dir, name, directoryUnresolved(invocation))
		if !ok || len(resolved) > maximumTrackedPathSize {
			continue
		}
		resolved = shell.ResolvePath(resolved)
		setFIFO(current.facts, resolved, true)
	}
}

func observeRM(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation, targets []string) {
	current := currentObservation(ctx)
	if current == nil {
		return
	}
	for _, target := range targets {
		resolved, ok := resolvedPath(invocation.Dir, target, directoryUnresolved(invocation))
		if ok {
			setFIFO(current.facts, shell.ResolvePath(resolved), false)
		}
	}
}

func observeCat(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) {
	current := currentObservation(ctx)
	if current == nil {
		return
	}
	current.handled = true
	current.facts.stream = streamFact{}
	name, ok := singleCatOperand(invocation.Args)
	if !ok {
		return
	}
	resolved, ok := resolvedPath(invocation.Dir, name, directoryUnresolved(invocation))
	if !ok {
		return
	}
	resolved = shell.ResolvePath(resolved)
	if fifoTracked(current.facts.fifos, resolved) {
		current.facts.stream = streamFact{stage: streamFIFO, fifo: resolved}
	}
}

func fifoCount(fifo *fifoFact) int {
	if fifo == nil {
		return 0
	}
	return fifo.count
}

func fifoTracked(fifo *fifoFact, name string) bool {
	for current := fifo; current != nil; current = current.previous {
		if current.name == name {
			return current.present
		}
	}
	return false
}

func setFIFO(facts *pathFacts, name string, present bool) {
	if len(name) > maximumTrackedPathSize || fifoTracked(facts.fifos, name) == present {
		return
	}
	if fifoCount(facts.fifos) >= maximumTrackedFIFOs {
		facts.fifos = nil
		facts.stream = streamFact{}
		if !present {
			return
		}
	}
	facts.fifos = &fifoFact{
		name:     name,
		present:  present,
		previous: facts.fifos,
		count:    fifoCount(facts.fifos) + 1,
	}
	if !present && facts.stream.fifo == name {
		facts.stream = streamFact{}
	}
}

func observeInteractiveShell(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation, interactive bool) {
	current := currentObservation(ctx)
	if current == nil {
		return
	}
	current.handled = true
	stream := current.facts.stream
	current.facts.stream = streamFact{}
	if stream.stage != streamFIFO || !interactive || !unresolvedPipelineInput(invocation, shell.Redirects()) {
		return
	}
	current.facts.stream = streamFact{stage: streamInteractiveShell, fifo: stream.fifo}
}

func observeNetcat(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) bool {
	current := currentObservation(ctx)
	if current == nil {
		return false
	}
	current.handled = true
	stream := current.facts.stream
	current.facts.stream = streamFact{}
	return stream.stage == streamInteractiveShell &&
		unresolvedPipelineInput(invocation, shell.Redirects()) &&
		redirectsStdoutTo(shell.Redirects(), stream.fifo)
}

func unresolvedPipelineInput(invocation *libcommand.Invocation, redirects []*libcommand.Redirect) bool {
	if invocation.Unresolved == nil || !invocation.Unresolved.Stdin || len(invocation.Stdin) != 0 {
		return false
	}
	for _, redirect := range redirects {
		if redirect.FD == 0 {
			return false
		}
	}
	return true
}

func redirectsStdoutTo(redirects []*libcommand.Redirect, target string) bool {
	for _, redirect := range redirects {
		if redirect.FD != 1 || redirect.Unresolved {
			continue
		}
		switch redirect.Operator {
		case ">", ">|", ">>":
			if path.Clean(redirect.Target) == target {
				return true
			}
		}
	}
	return false
}

func mkfifoOperands(arguments []*libcommand.Argument) []string {
	values, concrete := concreteArguments(arguments)
	if !concrete {
		return nil
	}
	operands := make([]string, 0, len(values))
	options := true
	for index := 0; index < len(values); index++ {
		argument := values[index]
		if options && argument == "--" {
			options = false
			continue
		}
		if options && (argument == "-m" || argument == "--mode") {
			if index+1 >= len(values) {
				return nil
			}
			index++
			continue
		}
		if options && strings.HasPrefix(argument, "--mode=") {
			continue
		}
		if options && strings.HasPrefix(argument, "-") {
			return nil
		}
		operands = append(operands, argument)
	}
	return operands
}

func singleCatOperand(arguments []*libcommand.Argument) (string, bool) {
	values, concrete := concreteArguments(arguments)
	if !concrete {
		return "", false
	}
	options := true
	operand := ""
	for _, argument := range values {
		if options && argument == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(argument, "-") {
			return "", false
		}
		if operand != "" || argument == "-" {
			return "", false
		}
		operand = argument
	}
	return operand, operand != ""
}
