package page

import (
	"fmt"
	"strings"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// chord is a key and the modifiers held with it.
type chord struct {
	key       string
	modifiers []chromedp.Modifier
}

var keyNames = map[string]string{
	"enter": kb.Enter, "return": kb.Enter, "tab": kb.Tab, "escape": kb.Escape, "esc": kb.Escape,
	"backspace": kb.Backspace, "delete": kb.Delete, "space": " ",
	"arrowup": kb.ArrowUp, "up": kb.ArrowUp, "arrowdown": kb.ArrowDown, "down": kb.ArrowDown,
	"arrowleft": kb.ArrowLeft, "left": kb.ArrowLeft, "arrowright": kb.ArrowRight, "right": kb.ArrowRight,
	"home": kb.Home, "end": kb.End, "pageup": kb.PageUp, "pagedown": kb.PageDown,
}

var modifierNames = map[string]chromedp.Modifier{
	"ctrl": chromedp.ModifierCtrl, "control": chromedp.ModifierCtrl,
	"shift": chromedp.ModifierShift,
	"alt":   chromedp.ModifierAlt, "option": chromedp.ModifierAlt,
	"meta": chromedp.ModifierMeta, "cmd": chromedp.ModifierMeta, "command": chromedp.ModifierMeta, "super": chromedp.ModifierMeta,
}

// parseKeys reads keys as the key member takes them: chords apart by
// spaces, each a key with the modifiers before it joined by +, such as
// "ctrl+a Delete".
func parseKeys(keys string) ([]chord, error) {
	var chords []chord
	for _, field := range strings.Fields(keys) {
		parts := strings.Split(field, "+")
		var c chord
		for _, m := range parts[:len(parts)-1] {
			mod, ok := modifierNames[strings.ToLower(m)]
			if !ok {
				return nil, fmt.Errorf("unknown modifier %q in %q", m, field)
			}
			c.modifiers = append(c.modifiers, mod)
		}
		name := parts[len(parts)-1]
		switch {
		case keyNames[strings.ToLower(name)] != "":
			c.key = keyNames[strings.ToLower(name)]
		case len([]rune(name)) == 1:
			c.key = name
		case len(name) >= 2 && (name[0] == 'F' || name[0] == 'f'):
			var n int
			if _, err := fmt.Sscanf(name[1:], "%d", &n); err != nil || n < 1 || n > 12 {
				return nil, fmt.Errorf("unknown key %q", name)
			}
			c.key = string(rune(0x800 + n))
		default:
			return nil, fmt.Errorf("unknown key %q", name)
		}
		chords = append(chords, c)
	}
	if len(chords) == 0 {
		return nil, fmt.Errorf("no key to press")
	}
	return chords, nil
}

// buttonModifiers returns the modifiers named, such as ctrl and shift.
func buttonModifiers(names []string) ([]chromedp.Modifier, error) {
	var mods []chromedp.Modifier
	for _, n := range names {
		m, ok := modifierNames[strings.ToLower(n)]
		if !ok {
			return nil, fmt.Errorf("unknown modifier %q", n)
		}
		mods = append(mods, m)
	}
	return mods, nil
}
