package runtime

import (
	"fmt"
	"maps"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type variables struct {
	data     map[string]expand.Variable
	unknown  map[string]struct{}
	versions map[string]uint64
	indexed  map[string]map[int]struct{}
	revision uint64
	// materializedBytes is the logical retained size of data, unknown, and
	// versions. It is copied with the maps and updated on every mutation.
	materializedBytes int
	shared            bool
}

type stateEnvironment struct {
	state     *State
	overrides *expansionOverrides
	maximum   int
}

func (environment *stateEnvironment) Get(name string) expand.Variable {
	if environment.overrides != nil {
		if value, exists := environment.overrides.values[name]; exists {
			return cloneVariable(value)
		}
	}
	switch name {
	case "?":
		exitCode, _ := environment.state.exitStatus.Data()
		return expand.Variable{Set: true, Kind: expand.String, Str: strconv.Itoa(exitCode)}
	case "-":
		return expand.Variable{Set: true, ReadOnly: true, Kind: expand.String, Str: shellOptionFlags(&environment.state.options)}
	case "$":
		return expand.Variable{Set: true, Kind: expand.String}
	case "!":
		if environment.state.backgroundPIDSet {
			return expand.Variable{Set: true, Kind: expand.String}
		}
		return expand.Variable{}
	case "SHELLOPTS":
		return expand.Variable{Set: true, ReadOnly: true, Kind: expand.String, Str: enabledShellOptions(&environment.state.options)}
	case "PIPESTATUS":
		values, _ := environment.state.pipelineStatusValues()
		return expand.Variable{Set: true, ReadOnly: true, Kind: expand.Indexed, List: values}
	}
	return environment.state.vars.Get(name)
}

func (environment *stateEnvironment) Each(yield func(string, expand.Variable) bool) {
	environment.state.vars.Each(yield)
}

func (environment *stateEnvironment) Set(name string, value expand.Variable) error {
	if name == "?" || name == "-" || name == "SHELLOPTS" || name == "PIPESTATUS" {
		return fmt.Errorf("special parameter %s is read-only", name)
	}
	if environment.overrides != nil {
		if reference, exists := environment.overrides.references[name]; exists {
			return setCollectionElement(environment.state, reference, value, environment.maximum)
		}
	}
	return environment.state.vars.Set(name, value)
}

type collectionReference struct {
	name        string
	index       int
	key         string
	associative bool
}

type expansionOverrides struct {
	values     map[string]expand.Variable
	references map[string]*collectionReference
}

func shellOptionFlags(options *shellOptions) string {
	var flags strings.Builder
	for _, option := range []struct {
		flag    byte
		enabled bool
	}{
		{flag: 'a', enabled: options.allExport},
		{flag: 'e', enabled: options.errexit},
		{flag: 'f', enabled: options.noGlob},
		{flag: 'h', enabled: true},
		{flag: 'u', enabled: options.noUnset},
		{flag: 'v', enabled: options.verbose},
		{flag: 'x', enabled: options.xtrace},
		{flag: 'B', enabled: true},
		{flag: 'E', enabled: options.errTrace},
		{flag: 'c', enabled: options.commandString},
	} {
		if option.enabled {
			flags.WriteByte(option.flag)
		}
	}
	return flags.String()
}

func enabledShellOptions(options *shellOptions) string {
	names := []string{"braceexpand", "hashall", "interactive-comments"}
	for _, option := range []struct {
		name    string
		enabled bool
	}{
		{name: "allexport", enabled: options.allExport},
		{name: "errexit", enabled: options.errexit},
		{name: "errtrace", enabled: options.errTrace},
		{name: "noglob", enabled: options.noGlob},
		{name: "nounset", enabled: options.noUnset},
		{name: "pipefail", enabled: options.pipefail},
		{name: "verbose", enabled: options.verbose},
		{name: "xtrace", enabled: options.xtrace},
	} {
		if option.enabled {
			names = append(names, option.name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ":")
}

func collectionElement(s *State, reference *collectionReference) expand.Variable {
	current := s.vars.Get(reference.name)
	result := expand.Variable{
		Kind:     expand.String,
		Local:    current.Local,
		Exported: current.Exported,
		ReadOnly: current.ReadOnly,
	}
	if reference.associative {
		if current.Kind != expand.Associative {
			return result
		}
		result.Str, result.Set = current.Map[reference.key]
		return result
	}
	if current.Kind != expand.Indexed {
		if reference.index == 0 && current.IsSet() {
			result.Set = true
			result.Str = current.String()
		}
		return result
	}
	if reference.index < 0 || reference.index >= len(current.List) {
		return result
	}
	if slots := s.vars.indexedSlots(reference.name); slots != nil {
		if _, present := slots[reference.index]; !present {
			return result
		}
	}
	result.Set = true
	result.Str = current.List[reference.index]
	return result
}

func setCollectionElement(s *State, reference *collectionReference, value expand.Variable, maximum int) error {
	current := s.vars.Get(reference.name)
	if current.ReadOnly {
		return fmt.Errorf("%s: readonly variable", reference.name)
	}
	unknown := s.vars.isUnknown(reference.name)
	if reference.associative {
		if current.Kind != expand.Associative {
			current.Set = true
			current.Kind = expand.Associative
			current.Str = ""
			current.List = nil
			current.Map = make(map[string]string)
		}
		if value.IsSet() {
			current.Map[reference.key] = value.String()
		} else {
			delete(current.Map, reference.key)
		}
		if variableValueBytes(&current) > normalizedMaxMemoryBytes(maximum) {
			return materialize.LimitError(normalizedMaxMemoryBytes(maximum))
		}
		s.vars.putWithCertainty(reference.name, current, unknown)
		return nil
	}

	if current.Kind != expand.Indexed {
		list := []string(nil)
		if current.IsSet() {
			list = []string{current.String()}
		}
		current.Set = true
		current.Kind = expand.Indexed
		current.Str = ""
		current.List = list
		current.Map = nil
	}
	slots := explicitIndexedSlots(s.vars.indexedSlots(reference.name), len(current.List))
	if value.IsSet() {
		var err error
		current.List, err = extendIndexedArray(current.List, reference.index, normalizedMaxMemoryBytes(maximum))
		if err != nil {
			return err
		}
		current.List[reference.index] = value.String()
		slots[reference.index] = struct{}{}
	} else if reference.index >= 0 && reference.index < len(current.List) {
		current.List[reference.index] = ""
		delete(slots, reference.index)
		for len(current.List) != 0 {
			last := len(current.List) - 1
			if _, present := slots[last]; present {
				break
			}
			current.List = current.List[:last]
		}
	}
	if variableValueBytes(&current) > normalizedMaxMemoryBytes(maximum) {
		return materialize.LimitError(normalizedMaxMemoryBytes(maximum))
	}
	s.vars.putIndexedWithCertainty(reference.name, current, unknown, normalizeIndexedSlots(slots, len(current.List)))
	return nil
}

func explicitIndexedSlots(slots map[int]struct{}, length int) map[int]struct{} {
	if slots != nil {
		return slots
	}
	result := make(map[int]struct{}, length)
	for index := range length {
		result[index] = struct{}{}
	}
	return result
}

func newVariables(initial map[string]string) *variables {
	values := &variables{
		data: make(map[string]expand.Variable, len(initial)+3),
	}
	for name, value := range initial {
		values.setInitial(name, expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: value})
	}
	setDefaultVariable(values, "HOME", "/", true)
	setDefaultVariable(values, "PWD", "/", true)
	setDefaultVariable(values, "PATH", "", true)
	setDefaultVariable(values, "OPTIND", "1", false)
	return values
}

func setDefaultVariable(values *variables, name, value string, exported bool) {
	if _, exists := values.data[name]; exists {
		return
	}
	values.setInitial(name, expand.Variable{Set: true, Exported: exported, Kind: expand.String, Str: value})
}

func (v *variables) setInitial(name string, value expand.Variable) {
	value = normalizeShellVariable(value)
	v.data[name] = value
	v.materializedBytes = addRetainedBytes(v.materializedBytes, variableEntryBytes(name, &value))
}

func (v *variables) Get(name string) expand.Variable {
	if value, ok := v.data[name]; ok {
		return cloneVariable(value)
	}
	if strings.HasPrefix(name, "HOME ") {
		return expand.Variable{Set: true, Kind: expand.String, Str: "/"}
	}
	return expand.Variable{}
}

func (v *variables) Each(yield func(string, expand.Variable) bool) {
	names := make([]string, 0, len(v.data))
	for name := range v.data {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !yield(name, cloneVariable(v.data[name])) {
			return
		}
	}
}

func (v *variables) Set(name string, value expand.Variable) error {
	if current := v.Get(name); current.ReadOnly && value.Kind != expand.KeepValue {
		return fmt.Errorf("%s: readonly variable", name)
	}
	if !value.IsSet() {
		v.delete(name)
		return nil
	}
	if value.Kind == expand.KeepValue {
		current := v.Get(name)
		unknown := v.isUnknown(name)
		current.Exported = value.Exported
		current.Local = value.Local
		current.ReadOnly = value.ReadOnly
		v.putIndexedWithCertainty(name, current, unknown, v.indexedSlots(name))
		return nil
	}
	var slots map[int]struct{}
	current := v.Get(name)
	if value.Kind == expand.Indexed && current.Kind == expand.Indexed && len(value.List) == len(current.List) {
		slots = v.indexedSlots(name)
		if slots != nil {
			for index := range value.List {
				if value.List[index] != current.List[index] {
					slots[index] = struct{}{}
				}
			}
		}
	}
	v.putIndexedWithCertainty(name, value, false, slots)
	return nil
}

func (v *variables) clone() *variables {
	v.shared = true
	return &variables{
		data:              v.data,
		unknown:           v.unknown,
		versions:          v.versions,
		indexed:           v.indexed,
		revision:          v.revision,
		materializedBytes: v.materializedBytes,
		shared:            true,
	}
}

func (v *variables) ensureMutable() {
	if !v.shared {
		return
	}
	// Get/lookup and put clone mutable values. No stored array or slot map is
	// edited in place, so unrelated values can remain shared between tables.
	v.data = maps.Clone(v.data)
	v.unknown = maps.Clone(v.unknown)
	v.versions = maps.Clone(v.versions)
	v.indexed = maps.Clone(v.indexed)
	v.shared = false
}

func (v *variables) lookup(name string) (expand.Variable, bool) {
	value, exists := v.data[name]
	return cloneVariable(value), exists
}

func (v *variables) names() []string {
	names := make([]string, 0, len(v.data))
	for name := range v.data {
		names = append(names, name)
	}
	return names
}

func (v *variables) put(name string, value expand.Variable) {
	v.putWithCertainty(name, value, false)
}

func (v *variables) putUnknown(name string, value expand.Variable) {
	v.putWithCertainty(name, value, true)
}

func (v *variables) putWithCertainty(name string, value expand.Variable, unknown bool) {
	v.putIndexedWithCertainty(name, value, unknown, nil)
}

func (v *variables) putIndexedWithCertainty(name string, value expand.Variable, unknown bool, slots map[int]struct{}) {
	value = normalizeShellVariable(value)
	v.ensureMutable()
	if previous, exists := v.data[name]; exists {
		v.materializedBytes -= variableEntryBytes(name, &previous)
	}
	if previousSlots, exists := v.indexed[name]; exists {
		v.materializedBytes -= indexedSlotsBytes(name, previousSlots)
	}
	v.data[name] = cloneVariable(value)
	v.materializedBytes = addRetainedBytes(v.materializedBytes, variableEntryBytes(name, &value))
	delete(v.indexed, name)
	if value.Kind == expand.Indexed {
		if sparse := normalizeIndexedSlots(slots, len(value.List)); sparse != nil {
			if v.indexed == nil {
				v.indexed = make(map[string]map[int]struct{})
			}
			v.indexed[name] = sparse
			v.materializedBytes = addRetainedBytes(v.materializedBytes, indexedSlotsBytes(name, sparse))
		}
	}
	_, wasUnknown := v.unknown[name]
	if unknown {
		if v.unknown == nil {
			v.unknown = make(map[string]struct{})
		}
		v.unknown[name] = struct{}{}
	} else {
		delete(v.unknown, name)
	}
	if unknown != wasUnknown {
		unknownBytes := materialize.EntryBytes + len(name)
		if unknown {
			v.materializedBytes = addRetainedBytes(v.materializedBytes, unknownBytes)
		} else {
			v.materializedBytes -= unknownBytes
		}
	}
	v.markWritten(name)
}

func (v *variables) assignWithCertainty(name string, value expand.Variable, unknown bool) {
	current := v.Get(name)
	value.Local = current.Local
	value.Exported = current.Exported
	value.ReadOnly = current.ReadOnly
	v.putWithCertainty(name, value, unknown)
}

func (v *variables) version(name string) uint64 {
	return v.versions[name]
}

func (v *variables) markWritten(name string) {
	v.revision++
	if v.versions == nil {
		v.versions = make(map[string]uint64)
	}
	if _, exists := v.versions[name]; !exists {
		v.materializedBytes = addRetainedBytes(v.materializedBytes, materialize.EntryBytes+len(name))
	}
	v.versions[name] = v.revision
}

func (v *variables) isUnknown(name string) bool {
	_, unknown := v.unknown[name]
	return unknown
}

func (v *variables) delete(name string) {
	v.ensureMutable()
	if previous, exists := v.data[name]; exists {
		v.materializedBytes -= variableEntryBytes(name, &previous)
	}
	if _, unknown := v.unknown[name]; unknown {
		v.materializedBytes -= materialize.EntryBytes + len(name)
	}
	if slots, exists := v.indexed[name]; exists {
		v.materializedBytes -= indexedSlotsBytes(name, slots)
	}
	delete(v.data, name)
	delete(v.unknown, name)
	delete(v.indexed, name)
	v.markWritten(name)
}

func (v *variables) indexedSlots(name string) map[int]struct{} {
	return cloneIndexedSlots(v.indexed[name])
}

func cloneIndexedSlots(slots map[int]struct{}) map[int]struct{} {
	if slots == nil {
		return nil
	}
	cloned := make(map[int]struct{}, len(slots))
	for index := range slots {
		cloned[index] = struct{}{}
	}
	return cloned
}

func normalizeIndexedSlots(slots map[int]struct{}, length int) map[int]struct{} {
	if slots == nil || length == 0 {
		return nil
	}
	normalized := make(map[int]struct{}, len(slots))
	for index := range slots {
		if index >= 0 && index < length {
			normalized[index] = struct{}{}
		}
	}
	if len(normalized) == length {
		for index := range length {
			if _, exists := normalized[index]; !exists {
				return normalized
			}
		}
		return nil
	}
	return normalized
}

func indexedSlotsBytes(name string, slots map[int]struct{}) int {
	return addRetainedBytes(materialize.EntryBytes+len(name), len(slots)*materialize.EntryBytes)
}

func (v *variables) exported() map[string]string {
	result := make(map[string]string)
	for name, value := range v.data {
		if value.Exported && value.IsSet() {
			result[name] = value.String()
		}
	}
	return result
}

func (v *variables) clearExported() {
	for _, name := range v.names() {
		value := v.Get(name)
		if !value.Exported {
			continue
		}
		value.Exported = false
		v.putIndexedWithCertainty(name, value, v.isUnknown(name), v.indexedSlots(name))
	}
}

func validateShellVariableName(name string) error {
	if !syntax.ValidName(name) {
		return fmt.Errorf("%s: not a valid identifier", name)
	}
	return nil
}

func assignShellVariable(s *State, name string, value expand.Variable, unknown bool) error {
	return assignShellIndexedVariable(s, name, value, unknown, nil)
}

func assignShellIndexedVariable(s *State, name string, value expand.Variable, unknown bool, slots map[int]struct{}) error {
	if err := validateShellVariableName(name); err != nil {
		return err
	}
	current := s.vars.Get(name)
	if current.ReadOnly {
		return fmt.Errorf("%s: readonly variable", name)
	}
	value.Local = current.Local
	value.Exported = value.Exported || current.Exported
	value.ReadOnly = current.ReadOnly
	s.vars.putIndexedWithCertainty(name, value, unknown, slots)
	return nil
}

func unsetShellVariable(s *State, name string) error {
	if err := validateShellVariableName(name); err != nil {
		return err
	}
	if s.vars.Get(name).ReadOnly {
		return fmt.Errorf("%s: readonly variable", name)
	}
	s.vars.delete(name)
	return nil
}

func cloneVariable(value expand.Variable) expand.Variable {
	if value.List != nil {
		value.List = append([]string{}, value.List...)
	}
	if value.Map != nil {
		cloned := make(map[string]string, len(value.Map))
		for key, element := range value.Map {
			cloned[key] = element
		}
		value.Map = cloned
	}
	return value
}

func normalizeShellVariable(value expand.Variable) expand.Variable {
	truncate := func(text string) string {
		index := strings.IndexByte(text, 0)
		if index < 0 {
			return text
		}
		return strings.Clone(text[:index])
	}
	value.Str = truncate(value.Str)
	for index := range value.List {
		value.List[index] = truncate(value.List[index])
	}
	if value.Map != nil {
		normalized := make(map[string]string, len(value.Map))
		for key, item := range value.Map {
			normalized[truncate(key)] = truncate(item)
		}
		value.Map = normalized
	}
	return value
}

func variableEntryBytes(name string, value *expand.Variable) int {
	return addRetainedBytes(materialize.EntryBytes+len(name), variableValueBytes(value))
}

func variableValueBytes(value *expand.Variable) int {
	if value == nil {
		return 0
	}
	total := 0
	switch value.Kind {
	case expand.Indexed:
		for _, item := range value.List {
			total = addRetainedBytes(total, materialize.EntryBytes+len(item))
		}
	case expand.Associative:
		for key, item := range value.Map {
			total = addRetainedBytes(total, materialize.EntryBytes+len(key)+len(item))
		}
	default:
		total = addRetainedBytes(total, len(value.Str))
	}
	return total
}

func addRetainedBytes(total, addition int) int {
	result, ok := materialize.Add(total, addition, math.MaxInt)
	if !ok {
		return math.MaxInt
	}
	return result
}
