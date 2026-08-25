package runtime

import "bytes"

// Uncertain keeps one representative value together with whether the complete
// value cannot be resolved. Values must not be mutated after they are attached
// to a command output or runtime state.
type Uncertain[T any] struct {
	Value      T
	Unresolved bool
}

type uncertain[T any] = Uncertain[T]

func newUncertain[T any](data T, unresolved bool) *uncertain[T] {
	return &Uncertain[T]{Value: data, Unresolved: unresolved}
}

func newCertain[T any](data T) *uncertain[T] {
	return newUncertain(data, false)
}

func newUnresolved[T any](data T) *uncertain[T] {
	return newUncertain(data, true)
}

// Resolved returns a value whose complete contents are known.
func Resolved[T any](value T) *Uncertain[T] {
	return newCertain(value)
}

// Unresolved returns a representative value whose complete contents are not
// known.
func Unresolved[T any](value T) *Uncertain[T] {
	return newUnresolved(value)
}

// Data returns the representative value and whether it is unresolved.
func (v *Uncertain[T]) Data() (T, bool) {
	return v.Value, v.Unresolved
}

func cloneUncertainBytes(value *uncertain[[]byte]) *uncertain[[]byte] {
	data, unresolved := value.Data()
	data = append([]byte(nil), data...)
	return newUncertain(data, unresolved)
}

func trimUncertainBytes(value *uncertain[[]byte], cutset string) *uncertain[[]byte] {
	data, unresolved := value.Data()
	data = bytes.TrimRight(data, cutset)
	return newUncertain(data, unresolved)
}
