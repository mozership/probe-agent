package scenario

import "strings"

// japaneseNames are the names a workflow may give Japanese by, in lower
// case.
var japaneseNames = []string{"japanese", "ja", "ja-jp", "ja_jp", "jp", "jpn", "nihongo", "日本語"}

// Japanese says whether language names Japanese, in which the agent writes
// its own words, as the title of a finding it made itself. In another
// language they are in English.
func Japanese(language string) bool {
	l := strings.ToLower(strings.TrimSpace(language))
	for _, n := range japaneseNames {
		if l == n {
			return true
		}
	}
	return false
}

// blockedReason is the reason of a test the tester was blocked in, with
// the summary of the tester, written in the language of the test.
func (r *run) blockedReason(summary string) string {
	if Japanese(r.cfg.Language) {
		return "テスターは先へ進めませんでした: " + summary
	}
	return "The tester was blocked: " + summary
}
