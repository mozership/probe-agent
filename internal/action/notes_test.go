package action

import (
	"strings"
	"testing"
	"time"

	"github.com/mozership/probe/actionrpc"

	"github.com/mozership/probe-agent/internal/scenario"
)

// parsedNotes returns the res.notes of the result of o, read as an action
// that posts them reads the notes context.
func parsedNotes(t *testing.T, r req, o scenario.Outcome) []actionrpc.Note {
	t.Helper()
	ret, err := result(r, o, files{Report: "/tmp/x/report.md"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	notes, err := actionrpc.ParseNotes(ret["res"].(map[string]any)["notes"])
	if err != nil {
		t.Fatalf("res.notes is not what Probe reads: %v", err)
	}
	return notes
}

func TestNotes_Persona(t *testing.T) {
	o := personaOutcome()
	o.Findings[0].URL = "http://a.test/notes/new"
	notes := parsedNotes(t, req{Persona: "p", Goal: "g"}, o)
	if len(notes) != 2 {
		t.Fatalf("notes = %+v, want one for each finding", notes)
	}
	high, low := notes[0], notes[1]
	if high.Level != actionrpc.NoteCritical || high.Title != "The note page fails" {
		t.Errorf("the note of a high finding = %+v", high)
	}
	want := "**Expected:** A form\n\n**Observed:** A 500\n\nerror · step 2 · http://a.test/notes/new\n\n> GET http://a.test/notes/new -> 500"
	if high.Body != want {
		t.Errorf("body =\n%s\nwant\n%s", high.Body, want)
	}
	if low.Level != actionrpc.NoteInfo || !strings.Contains(low.Body, "None of the evidence was found") || strings.Contains(low.Body, "> Welcom") {
		t.Errorf("the note of an unverified low finding = %+v, want it said and its quote left out", low)
	}
}

func TestNotes_PersonaWithNothingFound(t *testing.T) {
	o := personaOutcome()
	o.Verdict, o.Findings = scenario.Pass, []scenario.Finding{}
	if notes := parsedNotes(t, req{Persona: "p", Goal: "g"}, o); len(notes) != 0 {
		t.Errorf("notes = %+v, want none", notes)
	}
}

func TestNotes_Scenario(t *testing.T) {
	r := req{Task: "t", Expect: "e"}
	if notes := parsedNotes(t, r, outcome()); len(notes) != 0 {
		t.Errorf("a pass left notes %+v", notes)
	}

	o := outcome()
	o.Verdict, o.Reason = scenario.Fail, "The dashboard says nothing of a welcome."
	o.Evidence = []scenario.Evidence{{Quote: "Dashboard", Verified: true}, {Quote: "Hello, alice"}}
	notes := parsedNotes(t, r, o)
	want := "The dashboard says nothing of a welcome.\n\nThe tester stopped (done) at http://a.test/dashboard\n\n> Dashboard"
	if len(notes) != 1 || notes[0].Level != actionrpc.NoteCritical || notes[0].Title != "The expectation did not hold" || notes[0].Body != want {
		t.Errorf("the notes of a fail = %+v, want one critical with body\n%s", notes, want)
	}

	o = outcome()
	o.Verdict, o.Reason, o.Stopped, o.Evidence = scenario.Blocked, "A CAPTCHA stopped the tester.", scenario.StoppedBlocked, nil
	notes = parsedNotes(t, r, o)
	if len(notes) != 1 || notes[0].Level != actionrpc.NoteWarning || !strings.HasPrefix(notes[0].Body, "A CAPTCHA stopped the tester.") {
		t.Errorf("the notes of a blocked test = %+v, want one warning", notes)
	}
}

func TestNotes_TitleIsALine(t *testing.T) {
	long := strings.Repeat("word ", 40) + "\nend"
	got := title(long)
	if strings.Contains(got, "\n") || len([]rune(got)) != maxTitle || !strings.HasSuffix(got, "…") {
		t.Errorf("title() = %q, want one line of %d characters", got, maxTitle)
	}
	if title(" Short\ntitle ") != "Short title" {
		t.Errorf("title() of a short title = %q", title(" Short\ntitle "))
	}
}

func TestNotes_InJapanese(t *testing.T) {
	o := personaOutcome()
	o.Findings[0].URL = "http://a.test/notes/new"
	notes := parsedNotes(t, req{Persona: "p", Goal: "g", Language: "Japanese"}, o)
	want := "**期待:** A form\n\n**実際:** A 500\n\nerror · ステップ 2 · http://a.test/notes/new\n\n> GET http://a.test/notes/new -> 500"
	if len(notes) != 2 || notes[0].Body != want {
		t.Fatalf("notes = %+v, want the first with body\n%s", notes, want)
	}
	if !strings.Contains(notes[1].Body, "モデルの読み違い") {
		t.Errorf("the note of an unverified finding = %+v", notes[1])
	}

	for _, language := range []string{"japanese", " 日本語 ", "ja", "JA-JP"} {
		o = outcome()
		o.Verdict, o.Reason = scenario.Fail, "ダッシュボードに挨拶がありません。"
		o.Evidence = []scenario.Evidence{{Quote: "Dashboard", Verified: true}}
		notes = parsedNotes(t, req{Task: "t", Expect: "e", Language: language}, o)
		want = "ダッシュボードに挨拶がありません。\n\nテスターは http://a.test/dashboard で止まりました（done）\n\n> Dashboard"
		if len(notes) != 1 || notes[0].Title != "期待した結果になりませんでした" || notes[0].Body != want {
			t.Errorf("the notes of a fail in %q = %+v, want body\n%s", language, notes, want)
		}
	}

	o = outcome()
	o.Verdict, o.Reason, o.Evidence = scenario.Blocked, "Un CAPTCHA.", nil
	notes = parsedNotes(t, req{Task: "t", Expect: "e", Language: "French"}, o)
	if len(notes) != 1 || notes[0].Title != "The test could not be carried out" || !strings.Contains(notes[0].Body, "The tester stopped") {
		t.Errorf("the notes in a language they do not know = %+v, want the fixed words in English", notes)
	}
}
