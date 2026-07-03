// Package state persists gummi's machinery: feature records, workflow
// positions, session transcripts, and the FD sequence counter. Backed by
// SQLite (modernc.org/sqlite, no cgo) under .gummi/state/.
package state
