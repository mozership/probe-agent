package action

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mozership/probe/actionrpc"

	"github.com/mozership/probe-agent/internal/scenario"
)

// maxTitle bounds the title of a note, as a title is a line naming it.
const maxTitle = 120

// levels are the levels of the notes of findings of each severity.
var levels = map[string]string{
	"high":   actionrpc.NoteCritical,
	"medium": actionrpc.NoteWarning,
	"low":    actionrpc.NoteInfo,
}

// note is an entry of res.notes, as Probe reads it.
type note struct {
	Level string `json:"level"`
	Title string `json:"title,omitempty"`
	Body  string `json:"body"`
}

// words are the fixed words of the notes, in a language. What the model
// wrote in them is in the language it was told.
type words struct {
	failed, blocked    string
	expected, observed string
	step, steps        string
	stopped            string
	unverified         string
}

var english = words{
	failed:     "The expectation did not hold",
	blocked:    "The test could not be carried out",
	expected:   "**Expected:**",
	observed:   "**Observed:**",
	step:       "step ",
	steps:      "steps ",
	stopped:    "The tester stopped (%s) at %s",
	unverified: "_None of the evidence was found in what the test recorded, so the finding may be a misreading of the model._",
}

// translations are the words of the languages the notes know, by the
// names a workflow may give them, in lower case. A language not among
// them has its notes in its own language, with these words in English.
var translations = map[string]words{
	"japanese": japanese, "ja": japanese, "ja-jp": japanese, "日本語": japanese,
}

var japanese = words{
	failed:     "期待した結果になりませんでした",
	blocked:    "テストを実行できませんでした",
	expected:   "**期待:**",
	observed:   "**実際:**",
	step:       "ステップ ",
	steps:      "ステップ ",
	stopped:    "テスターは %[2]s で止まりました（%[1]s）",
	unverified: "_根拠の引用がテストの記録にひとつも見つからなかったため、モデルの読み違いかもしれません。_",
}

// wordsIn are the words of the notes in language.
func wordsIn(language string) words {
	if w, ok := translations[strings.ToLower(strings.TrimSpace(language))]; ok {
		return w
	}
	return english
}

// notesOf are the notes the test leaves for people, for a later step to
// post where they work: a note for each finding of a persona test, and one
// for a scenario test that failed or was blocked. A test that passed, with
// nothing found, leaves none. Their fixed words are in language, when the
// notes know it.
func notesOf(o scenario.Outcome, language string) []note {
	w := wordsIn(language)
	notes := []note{}
	if o.Mode == scenario.ModePersona {
		for _, f := range o.Findings {
			notes = append(notes, findingNote(f, w))
		}
		return notes
	}
	switch o.Verdict {
	case scenario.Fail:
		notes = append(notes, note{Level: actionrpc.NoteCritical, Title: w.failed, Body: verdictBody(o, w)})
	case scenario.Blocked:
		notes = append(notes, note{Level: actionrpc.NoteWarning, Title: w.blocked, Body: verdictBody(o, w)})
	}
	return notes
}

// findingNote writes f as a note of the level of its severity: what was
// expected and observed, where, and what it rests on.
func findingNote(f scenario.Finding, w words) note {
	var b strings.Builder
	if f.Expected != "" {
		fmt.Fprintf(&b, "%s %s\n\n", w.expected, oneLine(f.Expected))
	}
	fmt.Fprintf(&b, "%s %s\n\n", w.observed, oneLine(f.Observed))
	meta := []string{f.Category}
	if len(f.Steps) > 0 {
		steps := make([]string, len(f.Steps))
		for i, s := range f.Steps {
			steps[i] = fmt.Sprint(s)
		}
		meta = append(meta, plural(len(f.Steps), w.step, w.steps)+strings.Join(steps, ", "))
	}
	if f.URL != "" {
		// A bare URL, as Probe may write a secret in it as <secret:NAME>,
		// which would end an autolink.
		meta = append(meta, f.URL)
	}
	b.WriteString(strings.Join(meta, " · "))
	writeEvidence(&b, f.Evidence)
	if !f.Verified {
		b.WriteString("\n\n" + w.unverified)
	}
	level, ok := levels[f.Severity]
	if !ok {
		level = actionrpc.NoteWarning
	}
	return note{Level: level, Title: title(f.Title), Body: b.String()}
}

// verdictBody writes why a scenario test failed or was blocked, what it
// rests on, and where the tester left the browser.
func verdictBody(o scenario.Outcome, w words) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(o.Reason))
	if o.URL != "" {
		b.WriteString("\n\n" + fmt.Sprintf(w.stopped, o.Stopped, o.URL))
	}
	writeEvidence(&b, o.Evidence)
	return b.String()
}

// writeEvidence writes the quotes found in what the test recorded, which
// are what the pages showed; one not found there is the model's alone.
func writeEvidence(b *strings.Builder, evidence []scenario.Evidence) {
	for _, e := range evidence {
		if e.Verified {
			fmt.Fprintf(b, "\n\n> %s", oneLine(e.Quote))
		}
	}
}

// title makes s a line of at most maxTitle characters.
func title(s string) string {
	s = oneLine(s)
	if utf8.RuneCountInString(s) <= maxTitle {
		return s
	}
	return string([]rune(s)[:maxTitle-1]) + "…"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
