package page

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

func TestParseKeys(t *testing.T) {
	chords, err := parseKeys("ctrl+a Delete Enter x shift+Tab F5")
	if err != nil {
		t.Fatal(err)
	}
	want := []chord{
		{kb.Delete, nil},
		{kb.Enter, nil},
		{"x", nil},
		{kb.Tab, []chromedp.Modifier{chromedp.ModifierShift}},
		{string(rune(0x805)), nil},
	}
	if len(chords) != 6 || chords[0].key != "a" || len(chords[0].modifiers) != 1 || chords[0].modifiers[0] != chromedp.ModifierCtrl {
		t.Fatalf("chords %+v", chords)
	}
	for i, w := range want {
		c := chords[i+1]
		if c.key != w.key || len(c.modifiers) != len(w.modifiers) {
			t.Errorf("chord %d = %+v; want %+v", i+1, c, w)
		}
	}
	if kb.F5 != string(rune(0x805)) {
		t.Errorf("F5 is %q in kb", kb.F5)
	}

	for _, bad := range []string{"", "hyper+a", "Enterr", "F13"} {
		if _, err := parseKeys(bad); err == nil {
			t.Errorf("parseKeys(%q) took it", bad)
		}
	}
}
