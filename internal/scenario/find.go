package scenario

import (
	"sort"
	"strings"
	"unicode"
)

// maxFound is how many elements find returns at most.
const maxFound = 20

// roleWords are words a query may use for a role the lines name otherwise.
var roleWords = map[string]string{
	"field": "textbox", "input": "textbox", "box": "textbox", "text": "textbox",
	"dropdown": "combobox", "select": "combobox", "menu": "combobox",
	"tickbox": "checkbox", "check": "checkbox",
	"anchor": "link",
}

// find returns the lines of the page that match the words of query best,
// at most maxFound, in the order of the page. A line matches a word that
// it contains, or the role the word stands for.
func find(lines []string, query string) []string {
	words := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	type match struct {
		line  int
		score int
	}
	var matches []match
	for i, line := range lines {
		lower := strings.ToLower(line)
		score := 0
		for _, w := range words {
			if strings.Contains(lower, w) || (roleWords[w] != "" && strings.Contains(lower, roleWords[w])) {
				score++
			}
		}
		// Only an element, which has a ref, can be acted on.
		if score > 0 && strings.Contains(line, "[ref_") {
			matches = append(matches, match{i, score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	if len(matches) > maxFound {
		matches = matches[:maxFound]
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].line < matches[j].line })
	found := make([]string, len(matches))
	for i, m := range matches {
		found[i] = strings.TrimSpace(lines[m.line])
	}
	return found
}
