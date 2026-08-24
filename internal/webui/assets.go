// Package webui serves the embedded Meridian review UI.
package webui

import (
	"embed"
	"io/fs"
)

// dist always contains a small unavailable page so Go builds do not depend on
// first running the frontend toolchain. make ui-build replaces it with Vite's
// deterministic production output.
//
//go:embed dist
var embedded embed.FS

func assets() fs.FS {
	value, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return value
}
