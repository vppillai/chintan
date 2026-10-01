package cleanup

import (
	"errors"
	"strings"
	"testing"
)

// dropInvented alone: the count, the lift of a dropped parent's children,
// and the kept items' order. SplitOutput's tests cover the two steps
// together; this one pins the seam.
func TestDropInventedCountsAndLiftsChildren(t *testing.T) {
	const body = "- [ ] Add chickpeas and green gram into it.\n- [x] passport"
	items := []Item{
		{Text: "Pantry", Children: []Item{{Text: "Chickpeas"}, {Text: "Green gram"}}},
		{Text: "Make a list", Children: []Item{{Text: "Buy a pen"}}},
		{Text: "passport", Done: true},
	}
	kept, dropped := dropInvented(items, body)
	if dropped != 3 {
		t.Errorf("dropped = %d, want 3 (Pantry, Make a list, Buy a pen)", dropped)
	}
	if got := RenderTaskList(kept); got != "- [ ] Chickpeas\n- [ ] Green gram\n- [x] passport" {
		t.Errorf("kept = %q", got)
	}
	if kept, dropped := dropInvented([]Item{{Text: "Buy a pen"}}, body); len(kept) != 0 || dropped != 1 {
		t.Errorf("all invented: kept %d, dropped %d", len(kept), dropped)
	}
}

// tickSafety alone: each refusal carries its fixed sentence, and an answer
// that keeps every line and tick passes. The items are handed in as
// SplitOutput hands them, after dropInvented and reopenParents.
func TestTickSafetyNamesEachRefusal(t *testing.T) {
	const body = "- [ ] call the roofer\n- [x] passport\n- [ ] eggs"
	for name, tc := range map[string]struct {
		kept []Item
		want string
	}{
		"kept whole":           {[]Item{{Text: "Call the roofer"}, {Text: "Passport", Done: true}, {Text: "Eggs"}}, ""},
		"a done item lost":     {[]Item{{Text: "Call the roofer"}, {Text: "Eggs"}}, "a done item was lost"},
		"an open item lost":    {[]Item{{Text: "Call the roofer"}, {Text: "Passport", Done: true}}, "an open item was lost"},
		"a done item reopened": {[]Item{{Text: "Call the roofer"}, {Text: "Passport", Done: true}, {Text: "passport"}, {Text: "Eggs"}}, "a done item was reopened"},
		"an open item closed":  {[]Item{{Text: "Call the roofer"}, {Text: "Passport", Done: true}, {Text: "Eggs", Done: true}, {Text: "Buy eggs"}}, "an open item was closed"},
		"a done item invented": {[]Item{{Text: "Call the roofer", Done: true}, {Text: "Passport", Done: true}, {Text: "Eggs"}, {Text: "call the roofer"}}, "a done item was invented"},
	} {
		err := tickSafety(tc.kept, body)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: tickSafety = %v, want nil", name, err)
		case tc.want != "" && (!errors.Is(err, ErrNotATaskList) || !strings.HasSuffix(err.Error(), ": "+tc.want)):
			t.Errorf("%s: tickSafety = %v, want ErrNotATaskList %q", name, err, tc.want)
		}
	}
}
