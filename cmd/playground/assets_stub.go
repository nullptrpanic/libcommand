//go:build !playground_assets

package main

import (
	"errors"
	"io/fs"
)

func playgroundAssets() (fs.FS, error) {
	return nil, errors.New("playground assets are not embedded; build with make playground")
}
