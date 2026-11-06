// Package webui embeds the standalone plugin management interface.
package webui

import "embed"

// Explicit extensions prevent Go source and local tooling from entering the UI bundle.
//
//go:embed *.html *.js *.css *.png
var assets embed.FS

// ReadFile reads a bundled resource by its base filename.
func ReadFile(name string) ([]byte, error) {
	return assets.ReadFile(name)
}
