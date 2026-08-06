// Package web embeds the static client. There is no build step: the files
// here are served verbatim by the server binary.
package web

import "embed"

// FS holds the static assets. Add each new directory to the directive as it
// appears — go:embed fails the build on a pattern that matches nothing.
//
//go:embed index.html login.html lobby.html blueprints.html editor.html match.html help.html history.html js/*.js css/*.css
var FS embed.FS
