# UX pass source

`robocolony-ux.dc.html` is the design pass every screen is measured against.
It is committed verbatim and is not edited to match what we shipped; when the
two disagree, the gap is a bead (epic `rc-pt6`), not a doc fix.

Read it as source, not as a page. It was exported against an external
design-system stylesheet that is not vendored here, so opening the file raw
gives unstyled markup — every screen is still fully specified in it, because
the export writes its sizes, spacing and colours inline, and the tokens it
names (`--fg-0`, `--line-1`, `--mono`) are the same ones `web/css/app.css`
defines. Grep it for a screen id and read the measurements off the markup.

| Screen | Page |
|---|---|
| 1a Match · live | `web/match.html` |
| 1b Match · robot camera | `web/match.html` (POV mode) |
| 1c Match · replay | `web/match.html` (replay params) |
| 1d Program editor · cards | `web/editor.html` |
| 1e Program editor · code | `web/editor.html` (code view) |
| 1f Blueprint configurator | `web/blueprints.html` |
| 1g Lobby | `web/lobby.html` |
| 1h Colony dashboard | not built — `web/history.html` is the nearest thing |
| 1i First match | `web/index.html` |
