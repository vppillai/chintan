package service

import "testing"

// The marker is what makes the worker's append exactly-once, so the two
// properties below are load-bearing: stripping restores exactly the text the
// user would have had without markers, and carrying keeps every marker across
// an edit that never saw them.

func TestStripCaptureMarkersRestoresTheBodyWithoutThem(t *testing.T) {
	m := CaptureMarker("c_1")
	cases := map[string]struct{ in, want string }{
		"no markers":              {"plain text", "plain text"},
		"append onto content":     {"first\n\n" + m + "\nsecond", "first\n\nsecond"},
		"append into an empty":    {m + "\nonly paragraph", "only paragraph"},
		"two appends":             {"a\n\n" + m + "\nb\n\n" + CaptureMarker("c_2") + "\nc", "a\n\nb\n\nc"},
		"carried trailer":         {"edited\n" + m + "\n" + CaptureMarker("c_2"), "edited"},
		"carried then appended":   {"edited\n" + m + "\n\n" + CaptureMarker("c_3") + "\nlater", "edited\n\nlater"},
		"carried onto empty body": {"\n" + m, ""},
		"stacked at the start":    {m + "\n" + CaptureMarker("c_2") + "\ntext", "text"},
		"a lookalike is left":     {"see <!-- chintan:capture:bad id --> here", "see <!-- chintan:capture:bad id --> here"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := StripCaptureMarkers(tc.in); got != tc.want {
				t.Fatalf("StripCaptureMarkers(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestHasCaptureMarkerIsExactPerCapture(t *testing.T) {
	body := "a\n\n" + CaptureMarker("c_12") + "\nb"
	if !HasCaptureMarker(body, "c_12") {
		t.Fatal("the marker that is there was not found")
	}
	// c_1 is a prefix of c_12; the closing bracket keeps them apart.
	if HasCaptureMarker(body, "c_1") {
		t.Fatal("a prefix of another capture's id matched")
	}
	if HasCaptureMarker("b", "c_12") {
		t.Fatal("found a marker in a body that has none")
	}
}

func TestCarryCaptureMarkersKeepsTheGuardAcrossAnEdit(t *testing.T) {
	stored := "first\n\n" + CaptureMarker("c_1") + "\nsecond\n\n" + CaptureMarker("c_2") + "\nthird"
	edited := "the user rewrote everything"

	carried := CarryCaptureMarkers(stored, edited)
	for _, id := range []string{"c_1", "c_2"} {
		if !HasCaptureMarker(carried, id) {
			t.Fatalf("marker for %s was lost by the edit:\n%s", id, carried)
		}
	}
	// What the user reads back is exactly what they saved.
	if got := StripCaptureMarkers(carried); got != edited {
		t.Fatalf("stripped body = %q, want the edited text %q", got, edited)
	}
	// And the edit round-trips again with nothing accumulating.
	if again := CarryCaptureMarkers(carried, edited); again != carried {
		t.Fatalf("a second identical save changed the stored body:\n%q\n%q", carried, again)
	}
}

func TestCarryCaptureMarkersDoesNotTrustTheClientsMarkers(t *testing.T) {
	stored := "text"
	edited := "typed by hand\n" + CaptureMarker("c_forged")
	if got := CarryCaptureMarkers(stored, edited); got != "typed by hand" {
		t.Fatalf("a marker in the client's body survived: %q", got)
	}
	if got := CarryCaptureMarkers("", "fresh"); got != "fresh" {
		t.Fatalf("a body with no stored markers grew a trailer: %q", got)
	}
}

// The paragraph boundary is what deleting and moving a recording cut along, so
// every placement the worker and the carry can produce is pinned here, and the
// stripped view after a cut is checked against what the user would expect to
// see: the same body with exactly that paragraph gone.

func TestCutCaptureParagraphBoundary(t *testing.T) {
	m1, m2, m3 := CaptureMarker("c_1"), CaptureMarker("c_2"), CaptureMarker("c_3")
	cases := map[string]struct {
		body, cut        string
		wantRest, wantTx string
	}{
		"first paragraph opens the body": {
			body: m1 + "\nP1\n\n" + m2 + "\nP2", cut: "c_1",
			wantRest: m2 + "\nP2", wantTx: "P1",
		},
		"last paragraph": {
			body: m1 + "\nP1\n\n" + m2 + "\nP2", cut: "c_2",
			wantRest: m1 + "\nP1", wantTx: "P2",
		},
		"middle of three after typed content": {
			body: "intro\n\n" + m1 + "\nP1\n\n" + m2 + "\nP2\n\n" + m3 + "\nP3", cut: "c_2",
			wantRest: "intro\n\n" + m1 + "\nP1\n\n" + m3 + "\nP3", wantTx: "P2",
		},
		"only paragraph after typed content": {
			body: "intro\n\n" + m1 + "\nP1", cut: "c_1",
			wantRest: "intro", wantTx: "P1",
		},
		"only paragraph in the note": {
			body: m1 + "\nP1", cut: "c_1",
			wantRest: "", wantTx: "P1",
		},
		"multi-line paragraph": {
			body: m1 + "\nline one\nline two\n\n" + m2 + "\nP2", cut: "c_1",
			wantRest: m2 + "\nP2", wantTx: "line one\nline two",
		},
		// The user rewrote the paragraph in place. The boundary is positional,
		// so what is cut is whatever now stands between the markers, not the
		// words the transcript had.
		"edited paragraph that no longer matches the transcript": {
			body: "intro\n\n" + m1 + "\nnot what was said at all\n\n" + m2 + "\nP2", cut: "c_1",
			wantRest: "intro\n\n" + m2 + "\nP2", wantTx: "not what was said at all",
		},
		// After a save the markers are a trailer with no text of their own
		// (CarryCaptureMarkers). Cutting removes the marker and nothing else.
		"edited body, first carried marker": {
			body: "the user rewrote everything\n" + m1 + "\n" + m2, cut: "c_1",
			wantRest: "the user rewrote everything\n" + m2, wantTx: "",
		},
		"edited body, last carried marker": {
			body: "the user rewrote everything\n" + m1 + "\n" + m2, cut: "c_2",
			wantRest: "the user rewrote everything\n" + m1, wantTx: "",
		},
		"carried marker onto an empty body": {
			body: "\n" + m1, cut: "c_1",
			wantRest: "", wantTx: "",
		},
		"carried then appended, cut the carried": {
			body: "edited\n" + m1 + "\n\n" + m3 + "\nlater", cut: "c_1",
			wantRest: "edited\n\n" + m3 + "\nlater", wantTx: "",
		},
		"carried then appended, cut the appended": {
			body: "edited\n" + m1 + "\n\n" + m3 + "\nlater", cut: "c_3",
			wantRest: "edited\n" + m1, wantTx: "later",
		},
		"stacked at the start": {
			body: m1 + "\n" + m2 + "\ntext", cut: "c_1",
			wantRest: m2 + "\ntext", wantTx: "",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rest, text, found := CutCaptureParagraph(tc.body, tc.cut)
			if !found {
				t.Fatalf("marker for %s not found in %q", tc.cut, tc.body)
			}
			if rest != tc.wantRest {
				t.Errorf("rest = %q, want %q", rest, tc.wantRest)
			}
			if text != tc.wantTx {
				t.Errorf("text = %q, want %q", text, tc.wantTx)
			}
			if HasCaptureMarker(rest, tc.cut) {
				t.Errorf("the marker survived the cut: %q", rest)
			}
		})
	}
}

func TestCutCaptureParagraphLeavesABodyWithoutTheMarkerAlone(t *testing.T) {
	body := "text\n\n" + CaptureMarker("c_12") + "\nP"
	rest, text, found := CutCaptureParagraph(body, "c_1")
	if found || rest != body || text != "" {
		t.Fatalf("CutCaptureParagraph = (%q, %q, %v), want the body unchanged and not found", rest, text, found)
	}
}

// olderThan builds the before() an insert uses, from creation instants.
func olderThan(created map[string]string, moving string) func(string) bool {
	return func(id string) bool {
		at, ok := created[id]
		return ok && at > created[moving]
	}
}

func TestInsertCaptureParagraphKeepsChronologicalOrder(t *testing.T) {
	m1, m2, m3, mc := CaptureMarker("c_1"), CaptureMarker("c_2"), CaptureMarker("c_3"), CaptureMarker("c_new")
	created := map[string]string{"c_1": "t1", "c_2": "t2", "c_3": "t3"}
	cases := map[string]struct {
		body, at string
		want     string
	}{
		"into an empty note":                  {"", "t0", mc + "\nNew"},
		"after typed content with no markers": {"intro", "t0", "intro\n\n" + mc + "\nNew"},
		"before the first, which opens the body": {
			m1 + "\nP1\n\n" + m2 + "\nP2", "t0",
			mc + "\nNew\n\n" + m1 + "\nP1\n\n" + m2 + "\nP2",
		},
		"before the first, after typed content": {
			"intro\n\n" + m1 + "\nP1", "t0",
			"intro\n\n" + mc + "\nNew\n\n" + m1 + "\nP1",
		},
		"between two": {
			"intro\n\n" + m1 + "\nP1\n\n" + m3 + "\nP3", "t2",
			"intro\n\n" + m1 + "\nP1\n\n" + mc + "\nNew\n\n" + m3 + "\nP3",
		},
		"after the last": {
			m1 + "\nP1\n\n" + m2 + "\nP2", "t9",
			m1 + "\nP1\n\n" + m2 + "\nP2\n\n" + mc + "\nNew",
		},
		"same instant sorts after": {
			m1 + "\nP1\n\n" + m2 + "\nP2", "t1",
			m1 + "\nP1\n\n" + mc + "\nNew\n\n" + m2 + "\nP2",
		},
		"before a carried trailer keeps the trailer": {
			"edited\n" + m1, "t0",
			"edited\n\n" + mc + "\nNew\n" + m1,
		},
		"before a trailer carried onto an empty body": {
			"\n" + m1, "t0",
			mc + "\nNew\n" + m1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			created["c_new"] = tc.at
			got := InsertCaptureParagraph(tc.body, "c_new", "New", olderThan(created, "c_new"))
			if got != tc.want {
				t.Fatalf("InsertCaptureParagraph(%q) =\n%q\nwant\n%q", tc.body, got, tc.want)
			}
		})
	}
}

func TestInsertCaptureParagraphIgnoresMarkersItCannotDate(t *testing.T) {
	created := map[string]string{"c_1": "t1", "c_new": "t0"}
	body := CaptureMarker("c_unknown") + "\nOrphan\n\n" + CaptureMarker("c_1") + "\nP1"
	got := InsertCaptureParagraph(body, "c_new", "New", olderThan(created, "c_new"))
	want := CaptureMarker("c_unknown") + "\nOrphan\n\n" + CaptureMarker("c_new") + "\nNew\n\n" + CaptureMarker("c_1") + "\nP1"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

func TestInsertCaptureParagraphCarriesAnEmptyParagraphAsATrailer(t *testing.T) {
	body := "text\n\n" + CaptureMarker("c_1") + "\nP1"
	got := InsertCaptureParagraph(body, "c_new", "", func(string) bool { return true })
	if want := body + "\n" + CaptureMarker("c_new"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if StripCaptureMarkers(got) != StripCaptureMarkers(body) {
		t.Fatalf("a marker with no text changed what the user sees: %q", StripCaptureMarkers(got))
	}
}

// A cut followed by an insert at the same chronological position is the
// identity, which is what makes a compensated move leave no trace.
func TestCutThenInsertRoundTrips(t *testing.T) {
	created := map[string]string{"c_1": "t1", "c_2": "t2", "c_3": "t3"}
	body := "intro\n\n" + CaptureMarker("c_1") + "\nP1\n\n" + CaptureMarker("c_2") + "\nP2\n\n" + CaptureMarker("c_3") + "\nP3"
	for _, id := range []string{"c_1", "c_2", "c_3"} {
		rest, text, found := CutCaptureParagraph(body, id)
		if !found {
			t.Fatalf("%s not found", id)
		}
		if got := InsertCaptureParagraph(rest, id, text, olderThan(created, id)); got != body {
			t.Fatalf("round trip of %s changed the body:\n%q\n%q", id, got, body)
		}
	}
}

func TestCaptureMarkerIDsAreInBodyOrder(t *testing.T) {
	body := "x\n\n" + CaptureMarker("c_b") + "\nB\n\n" + CaptureMarker("c_a") + "\nA"
	got := CaptureMarkerIDs(body)
	if len(got) != 2 || got[0] != "c_b" || got[1] != "c_a" {
		t.Fatalf("CaptureMarkerIDs = %v", got)
	}
	if got := CaptureMarkerIDs("none"); len(got) != 0 {
		t.Fatalf("CaptureMarkerIDs(none) = %v", got)
	}
}
