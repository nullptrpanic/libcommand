package runtime

import "bytes"

// uncertain keeps one representative value together with whether the complete
// value cannot be resolved. Values are immutable after construction so cloned
// execution paths can share them safely.
type uncertain[T any] struct {
	data       T
	unresolved bool
}

func newCertain[T any](data T) *uncertain[T] {
	return &uncertain[T]{data: data}
}

func newUnresolved[T any](data T) *uncertain[T] {
	return &uncertain[T]{data: data, unresolved: true}
}

func (v *uncertain[T]) Data() (T, bool) {
	return v.data, v.unresolved
}

func cloneUncertainBytes(value *uncertain[[]byte]) *uncertain[[]byte] {
	data, unresolved := value.Data()
	data = append([]byte(nil), data...)
	if unresolved {
		return newUnresolved(data)
	}
	return newCertain(data)
}

func trimUncertainBytes(value *uncertain[[]byte], cutset string) *uncertain[[]byte] {
	data, unresolved := value.Data()
	data = bytes.TrimRight(data, cutset)
	if unresolved {
		return newUnresolved(data)
	}
	return newCertain(data)
}
