// Package cleanup builds the prompts that turn a transcript into text worth
// keeping — the per-recording cleanup, the checklist-item extraction and the
// whole-note views — and parses what the model sends back. Every prompt, its
// inputs and the guards after each reply are documented in
// docs/design/prompts.md; the item extraction is docs/design/checklists.md.
package cleanup
