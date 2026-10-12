// Package web is the built Svelte app, embedded in the binary. It lives next
// to the app's sources because //go:embed cannot reach outside its own
// directory; `make web` builds dist/ before the Go build.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
