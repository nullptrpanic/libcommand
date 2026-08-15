package runtime

import "mvdan.cc/sh/v3/syntax"

type location struct {
	line   uint
	column uint
}

var unknownLocation = &location{}

func sourceLocation(node syntax.Node) *location {
	if node == nil {
		return unknownLocation
	}
	position := node.Pos()
	if !position.IsValid() {
		return unknownLocation
	}
	return &location{
		line:   position.Line(),
		column: position.Col(),
	}
}
