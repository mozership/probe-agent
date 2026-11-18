package page

import "testing"

func TestKeyNames(t *testing.T) {
	got, err := keyNames("ctrl+a Delete Enter x shift+Tab F5 cmd+shift+up esc")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Control+a Delete Enter x Shift+Tab F5 Meta+Shift+ArrowUp Escape"; got != want {
		t.Errorf("keyNames = %q; want %q", got, want)
	}
	for _, bad := range []string{"", "hyper+a", "Enterr", "F13", "F05"} {
		if _, err := keyNames(bad); err == nil {
			t.Errorf("keyNames(%q) took it", bad)
		}
	}
}
