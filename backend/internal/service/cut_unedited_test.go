package service

import "testing"

// The source cut's three answers: the paragraph as copied is cut; one whose
// words moved since the copy is left, and reported; a body without the
// marker changes nothing.
func TestCutUneditedCutsOnlyTheParagraphAsCopied(t *testing.T) {
	body := InsertCaptureParagraph("typed first", "c1", "the dictated words", func(string) bool { return false })
	rest, cut, edited := cutUnedited(body, "c1", "the dictated words")
	if !cut || edited || rest != "typed first" {
		t.Errorf("as copied: rest %q, cut %v, edited %v", rest, cut, edited)
	}
	rest, cut, edited = cutUnedited(body, "c1", "the words as copied earlier")
	if cut || !edited || rest != body {
		t.Errorf("edited since: rest %q, cut %v, edited %v", rest, cut, edited)
	}
	rest, cut, edited = cutUnedited("typed first", "c1", "the dictated words")
	if cut || edited || rest != "typed first" {
		t.Errorf("no marker: rest %q, cut %v, edited %v", rest, cut, edited)
	}
}
