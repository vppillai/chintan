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
