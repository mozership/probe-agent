package page

import (
	"fmt"
	"strings"
)

// keyNamesOf are the names the model may give a key, as the key member
// takes them, with the name of KeyboardEvent.key probe-browser presses it
// by.
var keyNamesOf = map[string]string{
	"enter": "Enter", "return": "Enter", "tab": "Tab", "escape": "Escape", "esc": "Escape",
	"backspace": "Backspace", "delete": "Delete", "space": "Space",
	"arrowup": "ArrowUp", "up": "ArrowUp", "arrowdown": "ArrowDown", "down": "ArrowDown",
	"arrowleft": "ArrowLeft", "left": "ArrowLeft", "arrowright": "ArrowRight", "right": "ArrowRight",
	"home": "Home", "end": "End", "pageup": "PageUp", "pagedown": "PageDown",
}

// modifierNamesOf are the names the model may give a modifier, with the
// name probe-browser takes.
var modifierNamesOf = map[string]string{
	"ctrl": "Control", "control": "Control",
	"shift": "Shift",
	"alt":   "Alt", "option": "Alt",
	"meta": "Meta", "cmd": "Meta", "command": "Meta", "super": "Meta",
}

// keyNames reads keys as the key member takes them, chords apart by
// spaces, each a key with the modifiers before it joined by +, such as
// "ctrl+a Delete", and returns them as probe-browser's press takes them,
// as "Control+a Delete".
func keyNames(keys string) (string, error) {
	var chords []string
	for _, field := range strings.Fields(keys) {
		parts := strings.Split(field, "+")
		var names []string
		for _, m := range parts[:len(parts)-1] {
			mod, ok := modifierNamesOf[strings.ToLower(m)]
			if !ok {
				return "", fmt.Errorf("unknown modifier %q in %q", m, field)
			}
			names = append(names, mod)
		}
		name := parts[len(parts)-1]
		switch {
		case keyNamesOf[strings.ToLower(name)] != "":
			name = keyNamesOf[strings.ToLower(name)]
		case len([]rune(name)) == 1:
		case len(name) >= 2 && (name[0] == 'F' || name[0] == 'f'):
			var n int
			if _, err := fmt.Sscanf(name[1:], "%d", &n); err != nil || n < 1 || n > 12 || fmt.Sprint(n) != name[1:] {
				return "", fmt.Errorf("unknown key %q", name)
			}
			name = fmt.Sprintf("F%d", n)
		default:
			return "", fmt.Errorf("unknown key %q", name)
		}
		chords = append(chords, strings.Join(append(names, name), "+"))
	}
	if len(chords) == 0 {
		return "", fmt.Errorf("no key to press")
	}
	return strings.Join(chords, " "), nil
}
