package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var builtAssets embed.FS

func Assets() (fs.FS, error) {
	return fs.Sub(builtAssets, "dist")
}
