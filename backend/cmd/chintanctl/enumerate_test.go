package main

import (
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
)

// A row is its `data` blob. The promoted attributes alone no longer decode
// (the pre-2026-10 fallback is gone), a blob that names no id is an error
// rather than a record named after its sort key, and the two json:"-"
// fields are read from beside the blob.
func TestRowsDecodeFromTheBlobAlone(t *testing.T) {
	it := noteItem("tenantA", model.NoteIndex{ID: "n1", Title: "Roof", CleanedBody: "cleaned"})
	n, err := noteFromItem(it)
	if err != nil || n.ID != "n1" || n.Title != "Roof" || n.CleanedBody != "cleaned" {
		t.Fatalf("noteFromItem = %+v, %v", n, err)
	}
	it["data"] = StringAttr(`{"title":"no id"}`)
	if _, err := noteFromItem(it); err == nil || !strings.Contains(err.Error(), "names no id") {
		t.Errorf("a note blob without an id decoded: %v", err)
	}
	delete(it, "data")
	if _, err := noteFromItem(it); err == nil || !strings.Contains(err.Error(), "no data blob") {
		t.Errorf("a note row without data decoded: %v", err)
	}
	ci := captureItem(model.CaptureIndex{ID: "c1", UserID: "tenantA", NoteID: "n1", Status: model.StatusAppended})
	c, err := captureFromItem(ci)
	if err != nil || c.ID != "c1" || c.NoteID != "n1" {
		t.Fatalf("captureFromItem = %+v, %v", c, err)
	}
	delete(ci, "data")
	if _, err := captureFromItem(ci); err == nil || !strings.Contains(err.Error(), "no data blob") {
		t.Errorf("a capture row without data decoded: %v", err)
	}
}
