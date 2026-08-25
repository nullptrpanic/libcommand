package runtime

import (
	"fmt"
	"maps"
	"sort"
	"strconv"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type State struct {
	vars                *variables
	initialBytes        int
	user                string
	functions           map[string]*syntax.FuncDecl
	functionsBytes      int
	functionsShared     bool
	localScopes         []map[string]*savedVariable
	localScopesBytes    int
	localScopesShared   bool
	substitutionFrames  []*substitutionFrame
	substitutionBytes   int
	dir                 *uncertain[string]
	stdin               *uncertain[[]byte]
	stdout              *uncertain[[]byte]
	stderr              *uncertain[[]byte]
	fs                  *memoryFS
	issue               error
	exitStatus          *uncertain[int]
	pipelineStatuses    *uncertain[[]string]
	options             shellOptions
	signal              controlSignal
	signalDepth         int
	loopDepth           int
	loopExitStatuses    []*uncertain[int]
	funcDepth           int
	sourceDepth         int
	traps               map[string]string
	trapsBytes          int
	trapsShared         bool
	exitTrapInherited   bool
	backgroundPIDSet    bool
	commandStates       map[string][]uint64
	pathID              uint64
	parent              *State
	retainedParentBytes int
	frozen              bool
	frozenBytes         int
}

type shellOptions struct {
	errexit        bool
	errTrace       bool
	inheritErrexit bool
	allExport      bool
	noUnset        bool
	pipefail       bool
	noGlob         bool
	xtrace         bool
	verbose        bool
	globStar       bool
	dotGlob        bool
	noCaseGlob     bool
	nullGlob       bool
	extGlob        bool
	commandString  bool
}

type controlSignal uint8

const (
	signalNone controlSignal = iota
	signalBreak
	signalContinue
	signalReturn
	signalExit
)

type savedVariable struct {
	value          expand.Variable
	exists         bool
	unknown        bool
	indexedSlots   map[int]struct{}
	appliedVersion uint64
}

type substitutionResult struct {
	stdout     *uncertain[[]byte]
	exitStatus *uncertain[int]
}

type substitutionFrame struct {
	values            map[*syntax.CmdSubst]*substitutionResult
	processPaths      map[*syntax.ProcSubst]string
	processOutputs    map[string]*syntax.ProcSubst
	materializedBytes int
	lastExitStatus    *uncertain[int]
}

func initializeState(request *Request, maximum int) (*State, error) {
	dir := request.WorkingDir
	if dir == "" {
		dir = "/"
	}
	user := request.User
	if user == "" {
		user = "user"
	}
	vars := newVariables(request.Env)
	vars.put("PWD", expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: dir})
	vars.put("0", expand.Variable{Set: true, Kind: expand.String, Str: "command.sh"})
	fs := newMemoryFS(maximum)
	if err := fs.ensureDir(dir); err != nil {
		return nil, fmt.Errorf("initialize working directory %q: %w", dir, err)
	}
	fileNames := make([]string, 0, len(request.Files))
	for name := range request.Files {
		fileNames = append(fileNames, name)
	}
	sort.Strings(fileNames)
	for _, name := range fileNames {
		if err := fs.writeValueMode(name, request.Files[name], false, false, true); err != nil {
			return nil, fmt.Errorf("initialize file %q: %w", name, err)
		}
	}
	s := &State{
		vars:             vars,
		user:             user,
		functions:        make(map[string]*syntax.FuncDecl),
		dir:              newCertain(dir),
		stdin:            newCertain(append([]byte(nil), request.Stdin...)),
		stdout:           newCertain[[]byte](nil),
		stderr:           newCertain[[]byte](nil),
		exitStatus:       newCertain(0),
		fs:               fs,
		traps:            make(map[string]string),
		pipelineStatuses: newCertain([]string{"0"}),
		commandStates:    make(map[string][]uint64),
	}
	s.replacePositionalArguments(request.Args)
	s.initialBytes, _ = stateMaterialization(s)
	return s, nil
}

// newShellChild constructs the process state for a simulated bash or sh
// invocation. Only process attributes inherited by a fresh shell are copied;
// shell-local control, scope, trap, job, and status state starts at zero.
func newShellChild(parent *State) *State {
	vars := newVariables(parent.vars.exported())
	if home := parent.vars.Get("HOME"); !home.IsSet() || !home.Exported {
		vars.delete("HOME")
	}
	for name := range parent.vars.unknown {
		value := parent.vars.Get(name)
		if value.Exported && value.IsSet() {
			vars.putUnknown(name, vars.Get(name))
		}
	}
	directory, directoryUnresolved := parent.dir.Data()
	pwd := expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: directory}
	if directoryUnresolved || parent.vars.isUnknown("PWD") {
		vars.putUnknown("PWD", pwd)
	} else {
		vars.put("PWD", pwd)
	}
	return &State{
		vars:                vars,
		initialBytes:        parent.initialBytes,
		user:                parent.user,
		functions:           make(map[string]*syntax.FuncDecl),
		dir:                 parent.dir,
		stdin:               parent.stdin,
		stdout:              newCertain[[]byte](nil),
		stderr:              newCertain[[]byte](nil),
		exitStatus:          newCertain(0),
		fs:                  parent.fs.clone(),
		traps:               make(map[string]string),
		pipelineStatuses:    newCertain([]string{"0"}),
		pathID:              parent.pathID,
		parent:              parent.parent,
		retainedParentBytes: parent.retainedParentBytes,
	}
}

func (s *State) clone() *State {
	s.functionsShared = true
	s.localScopesShared = true
	s.trapsShared = true
	return &State{
		vars:                s.vars.clone(),
		initialBytes:        s.initialBytes,
		user:                s.user,
		functions:           s.functions,
		functionsBytes:      s.functionsBytes,
		functionsShared:     true,
		localScopes:         s.localScopes[:len(s.localScopes):len(s.localScopes)],
		localScopesBytes:    s.localScopesBytes,
		localScopesShared:   true,
		substitutionFrames:  cloneSubstitutionFrames(s.substitutionFrames),
		substitutionBytes:   s.substitutionBytes,
		dir:                 s.dir,
		stdin:               s.stdin,
		stdout:              s.stdout,
		stderr:              s.stderr,
		fs:                  s.fs.clone(),
		issue:               s.issue,
		exitStatus:          s.exitStatus,
		pipelineStatuses:    s.pipelineStatuses,
		options:             s.options,
		signal:              s.signal,
		signalDepth:         s.signalDepth,
		loopDepth:           s.loopDepth,
		loopExitStatuses:    s.loopExitStatuses[:len(s.loopExitStatuses):len(s.loopExitStatuses)],
		funcDepth:           s.funcDepth,
		sourceDepth:         s.sourceDepth,
		traps:               s.traps,
		trapsBytes:          s.trapsBytes,
		trapsShared:         true,
		exitTrapInherited:   s.exitTrapInherited,
		backgroundPIDSet:    s.backgroundPIDSet,
		commandStates:       cloneCommandStates(s.commandStates),
		pathID:              s.pathID,
		parent:              s.parent,
		retainedParentBytes: s.retainedParentBytes,
		frozen:              s.frozen,
		frozenBytes:         s.frozenBytes,
	}
}

func cloneCommandStates(source map[string][]uint64) map[string][]uint64 {
	result := make(map[string][]uint64, len(source))
	for name, values := range source {
		result[name] = append([]uint64(nil), values...)
	}
	return result
}

func (s *State) mutableTraps() map[string]string {
	if !s.trapsShared {
		return s.traps
	}
	cloned := make(map[string]string, len(s.traps))
	maps.Copy(cloned, s.traps)
	s.traps = cloned
	s.trapsShared = false
	return s.traps
}

func (s *State) setTrap(signal, command string) {
	previous, exists := s.traps[signal]
	if exists {
		s.trapsBytes -= len(previous)
	} else {
		s.trapsBytes = addRetainedBytes(s.trapsBytes, materialize.EntryBytes+len(signal))
	}
	s.mutableTraps()[signal] = command
	s.trapsBytes = addRetainedBytes(s.trapsBytes, len(command))
}

func (s *State) deleteTrap(signal string) {
	command, exists := s.traps[signal]
	if !exists {
		return
	}
	delete(s.mutableTraps(), signal)
	s.trapsBytes -= materialize.EntryBytes + len(signal) + len(command)
}

func (s *State) replacePositionalArguments(args []string) {
	for _, name := range []string{"#", "@", "*"} {
		s.vars.delete(name)
	}
	for _, name := range s.vars.names() {
		if index, err := strconv.Atoi(name); err == nil && index > 0 {
			s.vars.delete(name)
		}
	}
	s.vars.put("#", expand.Variable{Set: true, Kind: expand.String, Str: strconv.Itoa(len(args))})
	positional := append([]string{}, args...)
	s.vars.put("@", expand.Variable{Set: true, Kind: expand.Indexed, List: positional})
	s.vars.put("*", expand.Variable{Set: true, Kind: expand.Indexed, List: positional})
	for index, arg := range args {
		s.vars.put(strconv.Itoa(index+1), expand.Variable{Set: true, Kind: expand.String, Str: arg})
	}
}

func (s *State) pushLoop() {
	s.loopDepth++
	s.loopExitStatuses = append(s.loopExitStatuses[:len(s.loopExitStatuses):len(s.loopExitStatuses)], newCertain(0))
}

func (s *State) setLoopExitStatus(exitCode int, unresolved bool) {
	s.loopExitStatuses = append([]*uncertain[int](nil), s.loopExitStatuses...)
	if unresolved {
		s.loopExitStatuses[len(s.loopExitStatuses)-1] = newUnresolved(exitCode)
		return
	}
	s.loopExitStatuses[len(s.loopExitStatuses)-1] = newCertain(exitCode)
}

func (s *State) currentLoopExitStatus() (int, bool) {
	return s.loopExitStatuses[len(s.loopExitStatuses)-1].Data()
}

func (s *State) popLoop() {
	s.loopDepth--
	s.loopExitStatuses = s.loopExitStatuses[:len(s.loopExitStatuses)-1]
}

func (s *State) setExitCode(exitCode int) {
	s.setExitStatus(exitCode, false)
}

func (s *State) setUnknownExitCode() {
	s.setExitStatus(0, true)
}

func (s *State) setExitStatus(exitCode int, unresolved bool) {
	if unresolved {
		s.exitStatus = newUnresolved(exitCode)
	} else {
		s.exitStatus = newCertain(exitCode)
	}
	s.setPipelineStatusValues([]string{strconv.Itoa(exitCode)}, unresolved)
}

func (s *State) setPipelineStatusValues(values []string, unresolved bool) {
	values = append([]string{}, values...)
	if unresolved {
		s.pipelineStatuses = newUnresolved(values)
		return
	}
	s.pipelineStatuses = newCertain(values)
}

func (s *State) pipelineStatusValues() ([]string, bool) {
	values, unresolved := s.pipelineStatuses.Data()
	if len(values) == 0 {
		exitCode, exitUnresolved := s.exitStatus.Data()
		return []string{strconv.Itoa(exitCode)}, exitUnresolved
	}
	return append([]string(nil), values...), unresolved
}

func (s *State) resetOutput() {
	s.stdout = newCertain[[]byte](nil)
	s.stderr = newCertain[[]byte](nil)
}

func (s *State) pushLocalScope() {
	s.ensureLocalScopesMutable()
	s.localScopes = append(s.localScopes, make(map[string]*savedVariable))
	s.localScopesBytes = addRetainedBytes(s.localScopesBytes, materialize.EntryBytes)
}

func (s *State) saveLocal(name string) {
	if len(s.localScopes) == 0 {
		return
	}
	s.ensureLocalScopesMutable()
	scope := s.localScopes[len(s.localScopes)-1]
	if _, saved := scope[name]; !saved {
		value, exists := s.vars.lookup(name)
		saved := &savedVariable{
			value:        cloneVariable(value),
			exists:       exists,
			unknown:      s.vars.isUnknown(name),
			indexedSlots: s.vars.indexedSlots(name),
		}
		scope[name] = saved
		s.localScopesBytes = addRetainedBytes(s.localScopesBytes, variableEntryBytes(name, &saved.value))
		if saved.indexedSlots != nil {
			s.localScopesBytes = addRetainedBytes(s.localScopesBytes, indexedSlotsBytes(name, saved.indexedSlots))
		}
	}
}

func (s *State) popLocalScope() {
	s.ensureLocalScopesMutable()
	last := len(s.localScopes) - 1
	scope := s.localScopes[last]
	for name, saved := range scope {
		s.localScopesBytes -= variableEntryBytes(name, &saved.value)
		if saved.indexedSlots != nil {
			s.localScopesBytes -= indexedSlotsBytes(name, saved.indexedSlots)
		}
		if saved.exists {
			s.vars.putIndexedWithCertainty(name, saved.value, saved.unknown, saved.indexedSlots)
		} else {
			s.vars.delete(name)
		}
	}
	s.localScopesBytes -= materialize.EntryBytes
	s.localScopes = s.localScopes[:last]
}

func (s *State) mutableFunctions() map[string]*syntax.FuncDecl {
	if s.functionsShared {
		s.functions = cloneFunctions(s.functions)
		s.functionsShared = false
	}
	return s.functions
}

func (s *State) setFunction(name string, declaration *syntax.FuncDecl) {
	functions := s.mutableFunctions()
	if _, exists := functions[name]; !exists {
		s.functionsBytes = addRetainedBytes(s.functionsBytes, materialize.EntryBytes+len(name))
	}
	functions[name] = declaration
}

func (s *State) deleteFunction(name string) {
	if _, exists := s.functions[name]; !exists {
		return
	}
	delete(s.mutableFunctions(), name)
	s.functionsBytes -= materialize.EntryBytes + len(name)
}

func (s *State) ensureLocalScopesMutable() {
	if !s.localScopesShared {
		return
	}
	s.localScopes = cloneLocalScopes(s.localScopes)
	s.localScopesShared = false
}

func (s *State) pushSubstitutionFrame() {
	frame := &substitutionFrame{
		values:            make(map[*syntax.CmdSubst]*substitutionResult),
		processPaths:      make(map[*syntax.ProcSubst]string),
		processOutputs:    make(map[string]*syntax.ProcSubst),
		materializedBytes: materialize.EntryBytes,
	}
	s.substitutionFrames = append(s.substitutionFrames, frame)
	s.substitutionBytes = addRetainedBytes(s.substitutionBytes, frame.materializedBytes)
}

func (s *State) popSubstitutionFrame() {
	last := len(s.substitutionFrames) - 1
	s.substitutionBytes -= s.substitutionFrames[last].materializedBytes
	s.substitutionFrames[last] = nil
	s.substitutionFrames = s.substitutionFrames[:last]
}

func popSubstitutionFrameTree(s *State, visited map[*State]struct{}) {
	if s == nil {
		return
	}
	if _, exists := visited[s]; exists {
		return
	}
	visited[s] = struct{}{}
	if len(s.substitutionFrames) != 0 {
		s.popSubstitutionFrame()
	}
}

func (s *State) substitution(substitution *syntax.CmdSubst) (*substitutionResult, bool) {
	if len(s.substitutionFrames) == 0 {
		return nil, false
	}
	result, exists := s.substitutionFrames[len(s.substitutionFrames)-1].values[substitution]
	if !exists {
		return nil, false
	}
	resultCopy := *result
	resultCopy.stdout = cloneUncertainBytes(result.stdout)
	return &resultCopy, true
}

func (s *State) setSubstitution(substitution *syntax.CmdSubst, result *substitutionResult) {
	frame := s.substitutionFrames[len(s.substitutionFrames)-1]
	if previous, exists := frame.values[substitution]; exists {
		s.removeSubstitutionBytes(frame, substitutionResultBytes(previous))
	}
	resultCopy := *result
	resultCopy.stdout = cloneUncertainBytes(result.stdout)
	frame.values[substitution] = &resultCopy
	s.addSubstitutionBytes(frame, substitutionResultBytes(&resultCopy))
	frame.lastExitStatus = resultCopy.exitStatus
}

func (s *State) lastSubstitutionExitStatus() (exitCode int, unresolved bool, exists bool) {
	if len(s.substitutionFrames) == 0 {
		return 0, false, false
	}
	frame := s.substitutionFrames[len(s.substitutionFrames)-1]
	if frame.lastExitStatus == nil {
		return 0, false, false
	}
	exitCode, unresolved = frame.lastExitStatus.Data()
	return exitCode, unresolved, true
}

func (s *State) processSubstitution(substitution *syntax.ProcSubst) (string, bool) {
	if len(s.substitutionFrames) == 0 {
		return "", false
	}
	path, exists := s.substitutionFrames[len(s.substitutionFrames)-1].processPaths[substitution]
	return path, exists
}

func (s *State) setProcessSubstitution(substitution *syntax.ProcSubst, path string, output bool) {
	frame := s.substitutionFrames[len(s.substitutionFrames)-1]
	if previous, exists := frame.processPaths[substitution]; exists {
		s.removeSubstitutionBytes(frame, materialize.EntryBytes+len(previous))
	}
	frame.processPaths[substitution] = path
	s.addSubstitutionBytes(frame, materialize.EntryBytes+len(path))
	if output {
		if _, exists := frame.processOutputs[path]; !exists {
			s.addSubstitutionBytes(frame, materialize.EntryBytes+len(path))
		}
		frame.processOutputs[path] = substitution
	}
}

func (s *State) takeProcessOutputs() map[string]*syntax.ProcSubst {
	if len(s.substitutionFrames) == 0 {
		return nil
	}
	frame := s.substitutionFrames[len(s.substitutionFrames)-1]
	outputs := frame.processOutputs
	for path := range outputs {
		s.removeSubstitutionBytes(frame, materialize.EntryBytes+len(path))
	}
	frame.processOutputs = make(map[string]*syntax.ProcSubst)
	return outputs
}

func (s *State) addSubstitutionBytes(frame *substitutionFrame, addition int) {
	frame.materializedBytes = addRetainedBytes(frame.materializedBytes, addition)
	s.substitutionBytes = addRetainedBytes(s.substitutionBytes, addition)
}

func (s *State) removeSubstitutionBytes(frame *substitutionFrame, amount int) {
	frame.materializedBytes -= amount
	s.substitutionBytes -= amount
}

func cloneFunctions(source map[string]*syntax.FuncDecl) map[string]*syntax.FuncDecl {
	clone := make(map[string]*syntax.FuncDecl, len(source))
	for name, declaration := range source {
		clone[name] = declaration
	}
	return clone
}

func cloneLocalScopes(source []map[string]*savedVariable) []map[string]*savedVariable {
	clone := make([]map[string]*savedVariable, len(source))
	for index, scope := range source {
		clone[index] = make(map[string]*savedVariable, len(scope))
		for name, saved := range scope {
			savedCopy := *saved
			savedCopy.value = cloneVariable(saved.value)
			savedCopy.indexedSlots = cloneIndexedSlots(saved.indexedSlots)
			clone[index][name] = &savedCopy
		}
	}
	return clone
}

func cloneSubstitutionFrames(source []*substitutionFrame) []*substitutionFrame {
	frames := make([]*substitutionFrame, len(source))
	for index, frame := range source {
		values := make(map[*syntax.CmdSubst]*substitutionResult, len(frame.values))
		for substitution, result := range frame.values {
			resultCopy := *result
			resultCopy.stdout = cloneUncertainBytes(result.stdout)
			values[substitution] = &resultCopy
		}
		processPaths := make(map[*syntax.ProcSubst]string, len(frame.processPaths))
		for substitution, path := range frame.processPaths {
			processPaths[substitution] = path
		}
		processOutputs := make(map[string]*syntax.ProcSubst, len(frame.processOutputs))
		for path, substitution := range frame.processOutputs {
			processOutputs[path] = substitution
		}
		frames[index] = &substitutionFrame{
			values:            values,
			processPaths:      processPaths,
			processOutputs:    processOutputs,
			materializedBytes: frame.materializedBytes,
			lastExitStatus:    frame.lastExitStatus,
		}
	}
	return frames
}

func substitutionResultBytes(result *substitutionResult) int {
	stdout, _ := result.stdout.Data()
	return materialize.EntryBytes + len(stdout)
}
