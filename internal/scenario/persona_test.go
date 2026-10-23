package scenario

import (
	"strings"
	"testing"
	"time"

	"github.com/mozership/probe-agent/internal/llm"
	"github.com/mozership/probe-agent/internal/page"
)

func personaConfig() Config {
	return Config{
		Persona:  "A first-time visitor, signing up as {secret:EMAIL}, who is not technical.",
		Goal:     "Sign up and write a first note.",
		StartURL: "http://app.test:8080/",
		Secrets:  map[string]string{"EMAIL": "alice@example.com"},
		MaxSteps: 10,
		Timeout:  time.Minute,
		Save:     func(name string, _ []byte) string { return "/ev/" + name },
	}
}

func TestRun_PersonaReportsFindings(t *testing.T) {
	p := &fakePage{
		url:   "http://app.test:8080/welcome",
		title: "Welcome",
		read:  `link "Write your first note" [ref_1]`,
		text:  "Welcome, alice@example.com! Internal Server Error",
		network: []page.NetworkError{
			{Method: "GET", URL: "http://app.test:8080/notes/new", Status: 500},
		},
		requests: page.Requests{Count: 7, Slowest: []page.Request{
			{Method: "GET", URL: "http://app.test:8080/api/users?email=alice@example.com", Status: 200, DurationMS: 2400},
		}},
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			browserCall("1", "read_page", map[string]any{}),
			browserCall("2", "left_click", map[string]any{"target": map[string]any{"type": "ref", "ref": "ref_1"}}),
			toolCall("3", "note_finding", map[string]any{"title": "The note page fails", "severity": "high", "expected": "A form to write a note", "observed": "Internal Server Error"}),
			browserCall("4", "get_page_text", map[string]any{}),
			toolCall("5", "finish", map[string]any{"status": "done", "summary": "The note page said Internal Server Error."}),
		},
		{
			toolCall("r1", "review", map[string]any{
				"verdict":  "fail",
				"reason":   "No note could be written.",
				"evidence": []string{"Internal Server Error"},
				"findings": []map[string]any{
					{"title": "The welcome page says hi oddly", "severity": "bogus", "category": "content", "observed": "Hi there", "evidence": []string{"Hi there"}, "steps": []int{1}},
					{"title": "The note page fails", "severity": "high", "category": "error", "expected": "A form", "observed": "A 500", "evidence": []string{"GET http://app.test:8080/notes/new -> 500"}, "steps": []int{3, 2, 99, 2}},
				},
			}),
		},
	}}
	o, err := Run(t.Context(), personaConfig(), p, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Mode != ModePersona || o.Verdict != Fail || o.Stopped != StoppedDone {
		t.Fatalf("mode %s, verdict %s, stopped %s", o.Mode, o.Verdict, o.Stopped)
	}

	tester := m.chats[0]
	if !strings.Contains(tester.opts.Prompt, "signing up as {secret:EMAIL}") || strings.Contains(tester.opts.Prompt, "alice@") {
		t.Errorf("the tester's prompt: %s", tester.opts.Prompt)
	}
	if len(tester.opts.Tools) != 2 || tester.opts.Tools[0].Name != "note_finding" {
		t.Errorf("the tester's tools: %v", tester.opts.Tools)
	}
	if r := tester.turns[3].Results; len(r) != 1 || r[0].CallID != "3" || !strings.Contains(r[0].Text, "Noted N1") {
		t.Errorf("the note's result: %+v", r)
	}
	if len(o.Steps) != 3 {
		t.Errorf("a note took a step: %d steps", len(o.Steps))
	}

	reviewer := m.chats[1]
	for _, want := range []string{"<goal>\nSign up and write a first note.", "N1. [high] The note page fails (after step 2", "GET http://app.test:8080/notes/new -> 500", "email={secret:EMAIL} -> 200 in 2400 ms"} {
		if !strings.Contains(reviewer.opts.Prompt, want) {
			t.Errorf("the reviewer's prompt lacks %q:\n%s", want, reviewer.opts.Prompt)
		}
	}
	if strings.Contains(reviewer.opts.Prompt, "alice@") {
		t.Errorf("the reviewer saw a secret")
	}

	if len(o.Findings) != 2 {
		t.Fatalf("findings %+v", o.Findings)
	}
	high, odd := o.Findings[0], o.Findings[1]
	if high.Title != "The note page fails" || high.Severity != "high" || !high.Verified {
		t.Errorf("the first finding: %+v", high)
	}
	if len(high.Steps) != 2 || high.Steps[0] != 2 || high.Steps[1] != 3 {
		t.Errorf("its steps %v; want [2 3]", high.Steps)
	}
	if high.URL == "" || high.Screenshot != "/ev/step-02.jpg" {
		t.Errorf("its url %q, screenshot %q", high.URL, high.Screenshot)
	}
	if odd.Severity != "medium" || odd.Verified || odd.Evidence[0].Verified {
		t.Errorf("a finding quoting nothing recorded: %+v", odd)
	}

	if o.Metrics.Requests != 7 || o.Metrics.FailedRequests != 1 || o.Metrics.Steps != 3 {
		t.Errorf("metrics %+v", o.Metrics)
	}
	if s := o.Metrics.SlowestRequests; len(s) != 1 || strings.Contains(s[0].URL, "alice@") {
		t.Errorf("slowest %+v", s)
	}
}

func TestRun_PersonaBlockedKeepsFindings(t *testing.T) {
	p := &fakePage{url: "http://app.test:8080/", text: "Prove you are human"}
	m := &fakeModel{scripts: [][]llm.Reply{
		{toolCall("1", "finish", map[string]any{"status": "blocked", "summary": "A CAPTCHA."})},
		{toolCall("r1", "review", map[string]any{
			"verdict": "fail", "reason": "Not reached.", "evidence": []string{},
			"findings": []map[string]any{{"title": "A CAPTCHA on the first page", "severity": "medium", "category": "usability", "observed": "x", "evidence": []string{"Prove you are human"}, "steps": []int{}}},
		})},
	}}
	o, err := Run(t.Context(), personaConfig(), p, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Blocked || !strings.Contains(o.Reason, "A CAPTCHA.") || len(o.Findings) != 1 || !o.Findings[0].Verified {
		t.Errorf("verdict %s (%s), findings %+v", o.Verdict, o.Reason, o.Findings)
	}
}

func TestRun_PersonaWithNothingWrong(t *testing.T) {
	p := &fakePage{url: "http://app.test:8080/notes/1", text: "Your note is saved."}
	m := &fakeModel{scripts: [][]llm.Reply{
		{toolCall("1", "finish", map[string]any{"status": "done", "summary": "Saved."})},
		{toolCall("r1", "review", map[string]any{"verdict": "pass", "reason": "Saved.", "evidence": []string{"Your note is saved."}, "findings": []any{}})},
	}}
	o, err := Run(t.Context(), personaConfig(), p, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass || o.Findings == nil || len(o.Findings) != 0 {
		t.Errorf("verdict %s, findings %#v; want pass and an empty list", o.Verdict, o.Findings)
	}
}

func TestRun_ScenarioHasNoNoteTool(t *testing.T) {
	p := &fakePage{text: "Welcome"}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "note_finding", map[string]any{"title": "x", "severity": "low", "observed": "y"}),
			toolCall("2", "finish", map[string]any{"status": "done", "summary": "Welcome."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "ok", "evidence": []string{"Welcome"}})},
	}}
	o, err := Run(t.Context(), config(), p, m)
	if err != nil {
		t.Fatal(err)
	}
	if r := m.chats[0].turns[1].Results; len(r) != 1 || !r[0].IsError {
		t.Errorf("note_finding in a scenario test: %+v", r)
	}
	if o.Mode != ModeScenario || o.Findings != nil {
		t.Errorf("mode %s, findings %v", o.Mode, o.Findings)
	}
}
