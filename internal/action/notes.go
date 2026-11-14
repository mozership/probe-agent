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

// notesOf are the notes the test leaves for people, for a later step to
// post where they work: a note for each finding of a persona test, and one
// for a scenario test that failed or was blocked. A test that passed, with
// nothing found, leaves none.
func notesOf(o scenario.Outcome) []note {
	notes := []note{}
	if o.Mode == scenario.ModePersona {
		for _, f := range o.Findings {
			notes = append(notes, findingNote(f))
		}
		return notes
	}
	switch o.Verdict {
	case scenario.Fail:
		notes = append(notes, note{Level: actionrpc.NoteCritical, Title: "The expectation did not hold", Body: verdictBody(o)})
	case scenario.Blocked:
		notes = append(notes, note{Level: actionrpc.NoteWarning, Title: "The test could not be carried out", Body: verdictBody(o)})
	}
	return notes
}

// findingNote writes f as a note of the level of its severity: what was
// expected and observed, where, and what it rests on.
func findingNote(f scenario.Finding) note {
	var b strings.Builder
	if f.Expected != "" {
		fmt.Fprintf(&b, "**Expected:** %s\n\n", oneLine(f.Expected))
	}
	fmt.Fprintf(&b, "**Observed:** %s\n\n", oneLine(f.Observed))
	meta := []string{f.Category}
	if len(f.Steps) > 0 {
		steps := make([]string, len(f.Steps))
		for i, s := range f.Steps {
			steps[i] = fmt.Sprint(s)
		}
		meta = append(meta, plural(len(f.Steps), "step ", "steps ")+strings.Join(steps, ", "))
	}
	if f.URL != "" {
		// A bare URL, as Probe may write a secret in it as <secret:NAME>,
		// which would end an autolink.
		meta = append(meta, f.URL)
	}
	b.WriteString(strings.Join(meta, " · "))
	writeEvidence(&b, f.Evidence)
	if !f.Verified {
		b.WriteString("\n\n_None of the evidence was found in what the test recorded, so the finding may be a misreading of the model._")
	}
	level, ok := levels[f.Severity]
	if !ok {
		level = actionrpc.NoteWarning
	}
	return note{Level: level, Title: title(f.Title), Body: b.String()}
}

// verdictBody writes why a scenario test failed or was blocked, what it
// rests on, and where the tester left the browser.
func verdictBody(o scenario.Outcome) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(o.Reason))
	if o.URL != "" {
		fmt.Fprintf(&b, "\n\nThe tester stopped (%s) at %s", o.Stopped, o.URL)
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
