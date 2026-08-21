// Package webui embeds the production build of the Vue app.
// `make web-build` copies web/dist into this directory before compilation.
package webui

import (
	"embed"
)

//go:embed all:dist
var Dist embed.FS
