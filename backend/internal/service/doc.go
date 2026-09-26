// Package service holds the request-path use cases the handlers call: notes,
// captures, devices, settings, search, export and the ask queue. It is where
// the invariants the design docs state are enforced — the save-versus-append
// rules of docs/design/append-vs-autosave.md, then pins.md, checklists.md and
// inbox.md — so a handler is a decode, a call and an encode.
package service
