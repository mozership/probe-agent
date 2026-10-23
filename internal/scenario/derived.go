package scenario

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A value the tester typed that the task spells out, or that the tester
// made up, such as a name or a note, is the test's data and is typed again
// on a replay. A value it read off the screen, such as a code or a
// reference the application gave, or reckoned from the date, is this
// run's: the next run's application may give another, and typing the old
// one would replay a stale flow. Such a step is recorded as a gap, where
// the replay hands the test to the tester.

// The reasons a value is this run's.
const (
	// derivedPixels is a value that is not in the task, typed on a page the
	// tester took a screenshot of, whose text it may have read.
	derivedPixels = "pixels"
	// derivedWholeNode is a value that is the whole of what an element of
	// the page said.
	derivedWholeNode = "whole-node"
	// derivedToken is a value shaped like data, one word with a digit, that
	// the page showed as a word of its own.
	derivedToken = "minted-token"
	// derivedDate is a date or a time.
	derivedDate = "date"
)

var dateOrTime = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`\b\d{4}-\d{2}-\d{2}\b`,
	`\b\d{1,2}[/.-]\d{1,2}[/.-]\d{2,4}\b`,
	`\b\d{1,2}:\d{2}(?::\d{2})?\b`,
	`\b(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\.? \d{1,2}(?:st|nd|rd|th)?\b`,
	`\b\d{1,2}(?:st|nd|rd|th)? (?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\b`,
}, "|"))

// derived says why value is this run's, or "" when a replay types it
// again. instruction is what the test was asked, shown are the texts of
// the elements the pages showed, and pixels is whether the tester was
// shown a screenshot of the page.
func derived(value, instruction string, shown []string, pixels bool) string {
	needle := flatten(value)
	if needle == "" || strings.Contains(flatten(instruction), needle) {
		return ""
	}
	if pixels {
		return derivedPixels
	}
	dataShaped := !strings.ContainsFunc(needle, unicode.IsSpace) && strings.ContainsFunc(needle, unicode.IsDigit)
	token := false
	for _, s := range shown {
		s = flatten(s)
		if s == needle {
			return derivedWholeNode
		}
		if dataShaped && hasWord(s, needle) {
			token = true
		}
	}
	if token {
		return derivedToken
	}
	if dateOrTime.MatchString(value) {
		return derivedDate
	}
	return ""
}

func flatten(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// hasWord says whether text has needle as a word of its own: 123 is not in
// n20123.
func hasWord(text, needle string) bool {
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	for from := 0; ; {
		i := strings.Index(text[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		after, _ := utf8.DecodeRuneInString(text[i+len(needle):])
		if !word(before) && !word(after) {
			return true
		}
		from = i + 1
	}
}

// lineText matches a line of a read of the page, an element's role and
// its name or text, quoted.
var lineText = regexp.MustCompile(`^\s*[\w-]+ ("(?:[^"\\]|\\.)*")`)

// shownTexts returns the texts of the elements in a read of the page, or
// the lines of its text: what the tester may have read a value off.
func shownTexts(read string) []string {
	var out []string
	for _, line := range strings.Split(read, "\n") {
		if m := lineText.FindStringSubmatch(line); m != nil {
			var s string
			if json.Unmarshal([]byte(m[1]), &s) == nil && s != "" {
				out = append(out, s)
			}
			continue
		}
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
