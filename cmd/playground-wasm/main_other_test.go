//go:build !js || !wasm

package main

import "testing"

func TestNonWASMMainIsNoOp(t *testing.T) {
	main()
}
