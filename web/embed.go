// Package web embeds the deterministic frontend build so the Go service can
// host the single-page dashboard at "/".
package web

import (
	"embed"
	"io/fs"
)

// Dist holds the built frontend artifacts produced by "npm run build".
//
//go:embed dist
var Dist embed.FS

// Sub returns the dist/ subtree rooted so that "/" resolves to index.html.
func Sub() (fs.FS, error) {
	return fs.Sub(Dist, "dist")
}
