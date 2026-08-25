package runtime

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
)

func TestShellOptionVariablesReflectEnabledOptions(t *testing.T) {
	options := &shellOptions{
		allExport:     true,
		errexit:       true,
		noGlob:        true,
		noUnset:       true,
		verbose:       true,
		xtrace:        true,
		errTrace:      true,
		commandString: true,
		pipefail:      true,
	}
	if got := shellOptionFlags(options); got != "aefhuvxBEc" {
		t.Fatalf("flags = %q", got)
	}
	wantNames := "allexport:braceexpand:errexit:errtrace:hashall:interactive-comments:noglob:nounset:pipefail:verbose:xtrace"
	if got := enabledShellOptions(options); got != wantNames {
		t.Fatalf("SHELLOPTS = %q, want %q", got, wantNames)
	}
}

func TestCollectionElementsPreserveSparseAndAssociativeSemantics(t *testing.T) {
	state := newState(&Request{}, defaultMaxMemoryBytes)
	state.vars.put("scalar", expand.Variable{Set: true, Kind: expand.String, Str: "value", Exported: true})
	if got := collectionElement(state, &collectionReference{name: "scalar", index: 0}); !got.IsSet() || got.Str != "value" || !got.Exported {
		t.Fatalf("scalar[0] = %#v", got)
	}
	if got := collectionElement(state, &collectionReference{name: "scalar", index: 1}); got.IsSet() {
		t.Fatalf("scalar[1] = %#v", got)
	}

	state.vars.putIndexedWithCertainty("indexed", expand.Variable{Set: true, Kind: expand.Indexed, List: []string{"zero", "", "two"}}, false, map[int]struct{}{0: {}, 2: {}})
	if got := collectionElement(state, &collectionReference{name: "indexed", index: 1}); got.IsSet() {
		t.Fatalf("indexed[1] = %#v", got)
	}
	if got := collectionElement(state, &collectionReference{name: "indexed", index: 2}); !got.IsSet() || got.Str != "two" {
		t.Fatalf("indexed[2] = %#v", got)
	}

	state.vars.put("map", expand.Variable{Set: true, Kind: expand.Associative, Map: map[string]string{"key": "item"}})
	if got := collectionElement(state, &collectionReference{name: "map", associative: true, key: "key"}); !got.IsSet() || got.Str != "item" {
		t.Fatalf("map[key] = %#v", got)
	}
	if got := collectionElement(state, &collectionReference{name: "scalar", associative: true, key: "key"}); got.IsSet() {
		t.Fatalf("scalar associative lookup = %#v", got)
	}
}

func TestSetCollectionElementConvertsUpdatesAndUnsetsValues(t *testing.T) {
	state := newState(&Request{}, defaultMaxMemoryBytes)
	state.vars.putUnknown("value", expand.Variable{Set: true, Kind: expand.String, Str: "zero"})
	if err := setCollectionElement(state, &collectionReference{name: "value", index: 2}, expand.Variable{Set: true, Kind: expand.String, Str: "two"}, defaultMaxMemoryBytes); err != nil {
		t.Fatal(err)
	}
	indexed := state.vars.Get("value")
	if indexed.Kind != expand.Indexed || !reflect.DeepEqual(indexed.List, []string{"zero", "", "two"}) || !state.vars.isUnknown("value") {
		t.Fatalf("indexed value = %#v unknown=%v", indexed, state.vars.isUnknown("value"))
	}
	if slots := state.vars.indexedSlots("value"); !reflect.DeepEqual(slots, map[int]struct{}{0: {}, 2: {}}) {
		t.Fatalf("slots = %#v", slots)
	}
	if err := setCollectionElement(state, &collectionReference{name: "value", index: 2}, expand.Variable{}, defaultMaxMemoryBytes); err != nil {
		t.Fatal(err)
	}
	if got := state.vars.Get("value").List; !reflect.DeepEqual(got, []string{"zero"}) {
		t.Fatalf("trimmed list = %#v", got)
	}

	if err := setCollectionElement(state, &collectionReference{name: "map", associative: true, key: "key"}, expand.Variable{Set: true, Kind: expand.String, Str: "item"}, defaultMaxMemoryBytes); err != nil {
		t.Fatal(err)
	}
	if got := state.vars.Get("map"); got.Kind != expand.Associative || got.Map["key"] != "item" {
		t.Fatalf("map = %#v", got)
	}
	if err := setCollectionElement(state, &collectionReference{name: "map", associative: true, key: "key"}, expand.Variable{}, defaultMaxMemoryBytes); err != nil {
		t.Fatal(err)
	}
	if _, exists := state.vars.Get("map").Map["key"]; exists {
		t.Fatal("associative element was not removed")
	}

	state.vars.put("fixed", expand.Variable{Set: true, ReadOnly: true, Kind: expand.String, Str: "old"})
	if err := setCollectionElement(state, &collectionReference{name: "fixed", index: 0}, expand.Variable{Set: true, Kind: expand.String, Str: "new"}, defaultMaxMemoryBytes); err == nil {
		t.Fatal("readonly collection update succeeded")
	}
	if err := setCollectionElement(state, &collectionReference{name: "large", associative: true, key: "key"}, expand.Variable{Set: true, Kind: expand.String, Str: strings.Repeat("x", 128)}, 32); err == nil {
		t.Fatal("oversized collection update succeeded")
	}
}

func TestVariablesCopyOnWriteAttributesAndDeterministicIteration(t *testing.T) {
	values := newVariables(map[string]string{"HOME": "/custom", "Z": "last", "A": "first"})
	if values.Get("HOME").String() != "/custom" || values.Get("PWD").String() != "/" || values.Get("HOME root").String() != "/" {
		t.Fatalf("defaults = %#v", values.data)
	}
	var names []string
	values.Each(func(name string, _ expand.Variable) bool {
		names = append(names, name)
		return len(names) < 2
	})
	if !reflect.DeepEqual(names, []string{"A", "HOME"}) {
		t.Fatalf("iteration = %#v", names)
	}

	values.putIndexedWithCertainty("array", expand.Variable{Set: true, Kind: expand.Indexed, List: []string{"zero", "", "two"}}, true, map[int]struct{}{0: {}, 2: {}})
	copy := values.clone()
	copy.put("A", expand.Variable{Set: true, Kind: expand.String, Str: "changed"})
	if values.Get("A").String() != "first" || copy.Get("A").String() != "changed" {
		t.Fatalf("copy-on-write original=%q copy=%q", values.Get("A").String(), copy.Get("A").String())
	}
	copy.Set("array", expand.Variable{Set: true, Kind: expand.Indexed, List: []string{"zero", "one", "two"}})
	if slots := copy.indexedSlots("array"); slots != nil {
		t.Fatalf("updated slots = %#v", slots)
	}

	attributes := expand.Variable{Set: true, Kind: expand.KeepValue, Exported: true, Local: true, ReadOnly: true}
	if err := copy.Set("A", attributes); err != nil {
		t.Fatal(err)
	}
	if got := copy.Get("A"); got.String() != "changed" || !got.Exported || !got.Local || !got.ReadOnly {
		t.Fatalf("attributes = %#v", got)
	}
	if err := copy.Set("A", expand.Variable{Set: true, Kind: expand.String, Str: "blocked"}); err == nil {
		t.Fatal("readonly assignment succeeded")
	}
	if err := copy.Set("missing", expand.Variable{}); err != nil || copy.Get("missing").IsSet() {
		t.Fatalf("unset missing = %#v, %v", copy.Get("missing"), err)
	}

	exported := copy.exported()
	if exported["A"] != "changed" || exported["Z"] != "last" {
		t.Fatalf("exported = %#v", exported)
	}
	copy.clearExported()
	if copy.Get("A").Exported || copy.Get("Z").Exported {
		t.Fatalf("exports not cleared = %#v", copy.exported())
	}
}

func TestVariableValidationNormalizationAndAccounting(t *testing.T) {
	if err := validateShellVariableName("valid_name"); err != nil {
		t.Fatal(err)
	}
	if err := validateShellVariableName("bad-name"); err == nil {
		t.Fatal("invalid variable name succeeded")
	}
	state := newState(&Request{}, defaultMaxMemoryBytes)
	if err := assignShellVariable(state, "bad-name", expand.Variable{}, false); err == nil {
		t.Fatal("invalid assignment succeeded")
	}
	if err := unsetShellVariable(state, "bad-name"); err == nil {
		t.Fatal("invalid unset succeeded")
	}
	state.vars.put("fixed", expand.Variable{Set: true, ReadOnly: true, Kind: expand.String, Str: "old"})
	if err := assignShellVariable(state, "fixed", expand.Variable{Set: true, Kind: expand.String, Str: "new"}, false); err == nil {
		t.Fatal("readonly assignment succeeded")
	}
	if err := unsetShellVariable(state, "fixed"); err == nil {
		t.Fatal("readonly unset succeeded")
	}

	normalized := normalizeShellVariable(expand.Variable{
		Set:  true,
		Kind: expand.Associative,
		Str:  "scalar\x00ignored",
		List: []string{"one\x00ignored"},
		Map:  map[string]string{"key\x00ignored": "value\x00ignored"},
	})
	if normalized.Str != "scalar" || normalized.List[0] != "one" || normalized.Map["key"] != "value" {
		t.Fatalf("normalized = %#v", normalized)
	}
	cloned := cloneVariable(normalized)
	cloned.List[0] = "changed"
	cloned.Map["key"] = "changed"
	if normalized.List[0] != "one" || normalized.Map["key"] != "value" {
		t.Fatal("clone shares collection storage")
	}
	if variableValueBytes(nil) != 0 || variableValueBytes(&expand.Variable{Kind: expand.String, Str: "abc"}) != 3 {
		t.Fatal("scalar materialization accounting failed")
	}
	if addRetainedBytes(math.MaxInt, 1) != math.MaxInt {
		t.Fatal("retained-byte overflow did not saturate")
	}
}
