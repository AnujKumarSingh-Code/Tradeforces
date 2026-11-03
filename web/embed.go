// Package web embeds the single-page client served by the Go gateway.
package web

import "embed"

// FS holds index.html, app.js and style.css.
//
//go:embed index.html app.js style.css lightweight-charts.js
var FS embed.FS
