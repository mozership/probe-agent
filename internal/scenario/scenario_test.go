package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mozership/probe-agent/internal/llm"
	"github.com/mozership/probe-agent/internal/page"
)

// fakePage is a page that answers from fields, and records what it was
// asked to do.
type fakePage struct {
	url, title string
	read       string
	text       string
	typed      []string
	inputs     []any
	navigated  []string
	// afterClick, when set, is the URL a click takes the page to.
	afterClick string
}

func (p *fakePage) Navigate(_ context.Context, url string) error {
	p.navigated = append(p.navigated, url)
	if url != "back" {
		p.url = url
	}
	return nil
}
func (p *fakePage) Screenshot(context.Context) ([]byte, error) { return []byte{0xff, 0xd8}, nil }
func (p *fakePage) Zoom(context.Context, float64, float64, float64, float64) ([]byte, error) {
	return []byte{0xff, 0xd8}, nil
}
func (p *fakePage) Click(context.Context, page.Target, string, int, []string) error {
	if p.afterClick != "" {
		p.url = p.afterClick
	}
	return nil
}
func (p *fakePage) Hover(context.Context, page.Target) error                    { return nil }
func (p *fakePage) Scroll(context.Context, float64, float64, string, int) error { return nil }
func (p *fakePage) ScrollTo(context.Context, string) error                      { return nil }
func (p *fakePage) Type(_ context.Context, text string) error {
	p.typed = append(p.typed, text)
	return nil
}
func (p *fakePage) Key(context.Context, string, int) error                    { return nil }
func (p *fakePage) Read(context.Context, string, int, string) (string, error) { return p.read, nil }
func (p *fakePage) Text(context.Context) (string, error)                      { return p.text, nil }
func (p *fakePage) State(context.Context) (string, string, error)             { return p.url, p.title, nil }
func (p *fakePage) ConsoleErrors() []page.ConsoleError                        { return nil }
func (p *fakePage) NetworkErrors() []page.NetworkError                        { return nil }
func (p *fakePage) FormInput(_ context.Context, ref string, v any) (string, error) {
	p.inputs = append(p.inputs, v)
	return "Set the value of " + ref + ".", nil
}

// fakeModel answers each chat from a script of its own: the first chat
// started is the tester's, the second the judge's.
type fakeModel struct {
	scripts [][]llm.Reply
	chats   []*fakeChat
}

type fakeChat struct {
	opts   llm.ChatOptions
	script []llm.Reply
	turns  []llm.Turn
}

func (m *fakeModel) Name() string     { return "fake" }
func (m *fakeModel) Usage() llm.Usage { return llm.Usage{Requests: len(m.chats)} }
func (m *fakeModel) Chat(opts llm.ChatOptions) llm.Chat {
	c := &fakeChat{opts: opts}
	if len(m.chats) < len(m.scripts) {
		c.script = m.scripts[len(m.chats)]
	}
	m.chats = append(m.chats, c)
	return c
}

func (c *fakeChat) Send(_ context.Context, turn llm.Turn) (llm.Reply, error) {
	c.turns = append(c.turns, turn)
	if len(c.script) == 0 {
		return llm.Reply{}, fmt.Errorf("the script of the chat ran out")
	}
	r := c.script[0]
	c.script = c.script[1:]
	return r, nil
}

func browserCall(id, name string, input any) llm.Reply {
	b, _ := json.Marshal(input)
	return llm.Reply{Stop: llm.StopToolUse, Calls: []llm.Call{{ID: id, Name: name, Toolset: llm.ToolsetBrowser, Input: b}}}
}

func toolCall(id, name string, input any) llm.Reply {
	b, _ := json.Marshal(input)
	return llm.Reply{Stop: llm.StopToolUse, Calls: []llm.Call{{ID: id, Name: name, Input: b}}}
}

func config() Config {
	return Config{
		Task:     "Sign up as a new user with {secret:EMAIL} and the password {secret:PASSWORD}.",
		Expect:   "The account is created and the dashboard greets the user.",
		StartURL: "http://app.test:8080/",
		Secrets:  map[string]string{"PASSWORD": "hunter2-long", "EMAIL": "alice@example.com"},
		MaxSteps: 10,
		Timeout:  time.Minute,
	}
}

// lastResults returns the results the tester's chat was last sent.
func lastResults(c *fakeChat) []llm.Result {
	return c.turns[len(c.turns)-1].Results
}

func TestRun_PassesWithEvidenceThePagesShowed(t *testing.T) {
	p := &fakePage{
		title: "Sign up",
		read:  `textbox "Email" [ref_1]` + "\n" + `textbox "Password" [ref_2]`,
		text:  "Welcome, alice@example.com! Your account is ready.",
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			browserCall("1", "read_page", map[string]any{}),
			browserCall("2", "left_click", map[string]any{"target": map[string]any{"type": "ref", "ref": "ref_1"}}),
			browserCall("3", "type", map[string]any{"text": "{secret:EMAIL}"}),
			browserCall("4", "form_input", map[string]any{"target": map[string]any{"type": "ref", "ref": "ref_2"}, "value": "{secret:PASSWORD}"}),
			browserCall("5", "get_page_text", map[string]any{}),
			toolCall("6", "finish", map[string]any{"status": "done", "summary": "The page said Welcome."}),
		},
		{
			toolCall("j1", "verdict", map[string]any{
				"verdict":  "pass",
				"reason":   "The dashboard greets the user.",
				"evidence": []string{"Welcome, {secret:EMAIL}!", "Your plan is Gold"},
			}),
		},
	}}

	o, err := Run(context.Background(), config(), p, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass || o.Stopped != StoppedDone {
		t.Fatalf("verdict %s, stopped %s, reason %q; want pass, done", o.Verdict, o.Stopped, o.Reason)
	}
	want := []Evidence{{"Welcome, {secret:EMAIL}!", true}, {"Your plan is Gold", false}}
	if fmt.Sprint(o.Evidence) != fmt.Sprint(want) {
		t.Errorf("evidence %v; want %v", o.Evidence, want)
	}

	// The page got the values, and the model never saw them.
	if len(p.typed) != 1 || p.typed[0] != "alice@example.com" {
		t.Errorf("typed %q; want the email", p.typed)
	}
	if len(p.inputs) != 1 || p.inputs[0] != "hunter2-long" {
		t.Errorf("input %v; want the password", p.inputs)
	}
	for i, c := range m.chats {
		seen := c.opts.System + c.opts.Prompt
		for _, turn := range c.turns {
			for _, r := range turn.Results {
				seen += r.Text
			}
		}
		if strings.Contains(seen, "hunter2-long") || strings.Contains(seen, "alice@example.com") {
			t.Errorf("chat %d saw a secret:\n%s", i, seen)
		}
	}
	steps, _ := json.Marshal(o.Steps)
	if strings.Contains(string(steps), "hunter2-long") {
		t.Errorf("the steps hold a secret: %s", steps)
	}
	if len(o.Steps) != 5 || o.Steps[2].Tool != "type" || o.Steps[2].Input["text"] != "{secret:EMAIL}" {
		t.Errorf("steps %s", steps)
	}
	if !strings.Contains(m.chats[1].opts.Prompt, "{secret:EMAIL}") {
		t.Errorf("the judge was not told the task as written:\n%s", m.chats[1].opts.Prompt)
	}
}

func TestRun_APassThatQuotesNothingSeenIsBlocked(t *testing.T) {
	p := &fakePage{text: "Something went wrong."}
	m := &fakeModel{scripts: [][]llm.Reply{
		{toolCall("1", "finish", map[string]any{"status": "done", "summary": "Done."})},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "Looks fine.", "evidence": []string{"Welcome back"}})},
	}}
	o, err := Run(context.Background(), config(), p, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Blocked || !strings.Contains(o.Reason, "without quoting anything the pages showed") {
		t.Errorf("verdict %s (%s); want blocked for want of evidence", o.Verdict, o.Reason)
	}
}

func TestRun_AFailNeedsNoEvidence(t *testing.T) {
	p := &fakePage{text: "Something went wrong."}
	m := &fakeModel{scripts: [][]llm.Reply{
		{toolCall("1", "finish", map[string]any{"status": "done", "summary": "An error page."})},
		{toolCall("j1", "verdict", map[string]any{"verdict": "fail", "reason": "No dashboard.", "evidence": []string{}})},
	}}
	o, err := Run(context.Background(), config(), p, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Fail {
		t.Errorf("verdict %s (%s); want fail", o.Verdict, o.Reason)
	}
}

func TestRun_BlockedIsNotJudged(t *testing.T) {
	p := &fakePage{}
	m := &fakeModel{scripts: [][]llm.Reply{
		{toolCall("1", "finish", map[string]any{"status": "blocked", "summary": "A CAPTCHA."})},
	}}
	o, err := Run(context.Background(), config(), p, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Blocked || o.Stopped != StoppedBlocked || !strings.Contains(o.Reason, "A CAPTCHA.") {
		t.Errorf("verdict %s, stopped %s, reason %q", o.Verdict, o.Stopped, o.Reason)
	}
	if len(m.chats) != 1 {
		t.Errorf("%d chats; the judge should not have been asked", len(m.chats))
	}
}

func TestRun_StopsAtMaxStepsAndIsStillJudged(t *testing.T) {
	p := &fakePage{text: "Welcome"}
	var tester []llm.Reply
	for i := range 5 {
		tester = append(tester, browserCall(fmt.Sprint(i), "screenshot", map[string]any{}))
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		tester,
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "Greeted.", "evidence": []string{"Welcome"}})},
	}}
	cfg := config()
	cfg.MaxSteps = 3
	o, err := Run(context.Background(), cfg, p, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Stopped != StoppedMaxSteps || len(o.Steps) != 3 {
		t.Errorf("stopped %s after %d steps; want max_steps after 3", o.Stopped, len(o.Steps))
	}
	if o.Verdict != Pass {
		t.Errorf("verdict %s; want the judge's pass", o.Verdict)
	}
}

func TestRun_KeepsToTheHosts(t *testing.T) {
	p := &fakePage{afterClick: "https://evil.test/phish"}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			browserCall("1", "navigate", map[string]any{"url": "https://other.test/"}),
			browserCall("2", "left_click", map[string]any{"target": map[string]any{"type": "coordinate", "x": 10, "y": 10}}),
			toolCall("3", "finish", map[string]any{"status": "blocked", "summary": "Left the site."}),
		},
	}}
	o, err := Run(context.Background(), config(), p, m)
	if err != nil {
		t.Fatal(err)
	}
	tester := m.chats[0]
	nav := tester.turns[1].Results[0]
	if !nav.IsError || !strings.Contains(nav.Text, "not on a host this test may open") {
		t.Errorf("navigating to another host: %+v", nav)
	}
	click := tester.turns[2].Results[0]
	if !click.IsError || !strings.Contains(click.Text, "went back") {
		t.Errorf("a click that left the hosts: %+v", click)
	}
	if fmt.Sprint(p.navigated) != "[http://app.test:8080/ back]" {
		t.Errorf("navigated %v; want the start page, then back", p.navigated)
	}
	if !strings.Contains(o.Steps[1].Error, "evil.test") {
		t.Errorf("the step does not record leaving the hosts: %+v", o.Steps[1])
	}
}

func TestRun_AnUnknownSecretIsNotTyped(t *testing.T) {
	p := &fakePage{}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			browserCall("1", "type", map[string]any{"text": "{secret:TOKEN}"}),
			toolCall("2", "finish", map[string]any{"status": "blocked", "summary": "No token."}),
		},
	}}
	if _, err := Run(context.Background(), config(), p, m); err != nil {
		t.Fatal(err)
	}
	if len(p.typed) != 0 {
		t.Errorf("typed %q", p.typed)
	}
	r := m.chats[0].turns[1].Results[0]
	if !r.IsError || !strings.Contains(r.Text, "no secret is named TOKEN") || !strings.Contains(r.Text, "{secret:PASSWORD}") {
		t.Errorf("result %+v", r)
	}
}

func TestRun_NudgesATesterThatStopsWithoutFinishing(t *testing.T) {
	p := &fakePage{text: "Welcome"}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			{Stop: llm.StopEndTurn, Text: "I think I'm done."},
			toolCall("1", "finish", map[string]any{"status": "done", "summary": "Done."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "ok", "evidence": []string{"Welcome"}})},
	}}
	o, err := Run(context.Background(), config(), p, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Stopped != StoppedDone || !strings.Contains(m.chats[0].turns[1].Text, "call finish") {
		t.Errorf("stopped %s; turns %+v", o.Stopped, m.chats[0].turns)
	}
}

func TestRun_TellsTheModelOfANewPage(t *testing.T) {
	p := &fakePage{title: "Start", afterClick: "http://app.test:8080/dashboard"}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			browserCall("1", "left_click", map[string]any{"target": map[string]any{"type": "ref", "ref": "ref_1"}}),
			browserCall("2", "hover", map[string]any{"target": map[string]any{"type": "ref", "ref": "ref_1"}}),
			toolCall("3", "finish", map[string]any{"status": "blocked", "summary": "x"}),
		},
	}}
	if _, err := Run(context.Background(), config(), p, m); err != nil {
		t.Fatal(err)
	}
	click := m.chats[0].turns[1].Results[0]
	if len(click.Tabs) != 1 || click.Tabs[0].URL != "http://app.test:8080/dashboard" || !click.Tabs[0].Active {
		t.Errorf("the click that changed the page reported tabs %+v", click.Tabs)
	}
	if hover := m.chats[0].turns[2].Results[0]; len(hover.Tabs) != 0 {
		t.Errorf("the hover that changed nothing reported tabs %+v", hover.Tabs)
	}
}

func TestRun_TheTesterIsNotOfferedDisabledMembers(t *testing.T) {
	m := &fakeModel{scripts: [][]llm.Reply{{toolCall("1", "finish", map[string]any{"status": "blocked", "summary": "x"})}}}
	if _, err := Run(context.Background(), config(), &fakePage{}, m); err != nil {
		t.Fatal(err)
	}
	b := m.chats[0].opts.Browser
	if b == nil || !strings.Contains(strings.Join(b.Disabled, ","), "javascript_exec") {
		t.Errorf("browser toolset %+v", b)
	}
	if !strings.Contains(m.chats[0].opts.System, "app.test:8080") {
		t.Errorf("the tester is not told the host it may open:\n%s", m.chats[0].opts.System)
	}
}
