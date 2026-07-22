// Package infra holds the outermost layer: frameworks, drivers, and process
// wiring. It reads configuration, opens the database pool, supplies the real
// clock, and runs the HTTP server and background reaper.
//
// Nothing inward depends on this package — it is the last thing constructed and
// the first thing that would be swapped when the runtime environment changes.
package infra
