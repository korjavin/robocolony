// Package web embeds the static client. There is no build step: the files
// here are served verbatim by the server binary.
package web

import "embed"

// FS holds the static assets. Add css/js to the directive when they exist —
// go:embed fails the build on a pattern that matches nothing.
//
//go:embed index.html login.html lobby.html
var FS embed.FS
