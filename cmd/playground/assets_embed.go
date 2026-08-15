//go:build playground_assets

package main

import (
	"embed"
	"io/fs"
)

//go:embed all:.assets
var embeddedAssets embed.FS

func playgroundAssets() (fs.FS, error) {
	return fs.Sub(embeddedAssets, ".assets")
}
