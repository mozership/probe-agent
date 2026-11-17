package scenario

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mozership/probe-agent/internal/llm"
)

// fakeIOS answers the calls of probe-ios from a tree, and records the
// actions of each call.
type fakeIOS struct {
	t     *testing.T
	tree  string
	text  string
	calls [][]map[string]any
	// fail fails the actions of the selector.
	fail string
}

func (f *fakeIOS) target() *IOS {
	return &IOS{App: "com.example.app", Device: "iPhone 17", Call: f.call}
}

func (f *fakeIOS) call(_ context.Context, actions []map[string]any) (map[string]any, error) {
	f.calls = append(f.calls, actions)
	results := map[string]any{}
	paths := map[string]any{}
	for i, a := range actions {
		if a["selector"] != nil && a["selector"] == f.fail {
			return nil, errors.New("actions[" + string(rune('0'+i)) + "] tap: no element matches " + f.fail)
		}
		switch a["name"] {
		case "tree":
			results["tree"] = f.tree
		case "text":
			results["text"] = f.text
		case "screenshot":
			path := filepath.Join(f.t.TempDir(), "shot.png")
			if err := os.WriteFile(path, []byte{0x89, 'P', 'N', 'G'}, 0o600); err != nil {
				f.t.Fatal(err)
			}
			paths["screenshot"] = path
		}
	}
	return map[string]any{"code": 0, "results": results, "filepaths": paths, "device": "UDID-1"}, nil
}

func (f *fakeIOS) names(call int) []string {
	var n []string
	for _, a := range f.calls[call] {
		n = append(n, a["name"].(string))
	}
	return n
}

func TestRun_DrivesAnIOSApp(t *testing.T) {
	ios := &fakeIOS{t: t, tree: `Application, label: 'Example'
  TextField, identifier: 'email'
  Button, label: 'Sign in'`}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "ios_type", map[string]any{"selector": "email", "value": "{secret:EMAIL}"}),
			toolCall("2", "ios_tap", map[string]any{"selector": `type=button label="Sign in"`}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Signed in."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "Signed in.", "evidence": []string{"label: 'Sign in'"}})},
	}}
	var saved []string
	cfg := config()
	cfg.StartURL = ""
	cfg.Save = func(name string, _ []byte) string { saved = append(saved, name); return name }

	o, err := Run(t.Context(), cfg, Targets{IOS: ios.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass {
		t.Fatalf("verdict %s: %s", o.Verdict, o.Reason)
	}

	// The app is opened, its runner built, before the model starts.
	if got := ios.names(0); !slices.Equal(got, []string{"activate", "tree"}) {
		t.Errorf("the first call %v, want activate and tree", got)
	}
	// A move reads the tree after it, and a screenshot as the model sees
	// images.
	if got := ios.names(1); !slices.Equal(got, []string{"type", "tree", "screenshot"}) {
		t.Errorf("the call of ios_type %v", got)
	}
	if v := ios.calls[1][0]["value"]; v != "alice@example.com" {
		t.Errorf("typed %v, want the value of the secret", v)
	}
	if got := ios.names(2); !slices.Equal(got, []string{"tap", "tree", "screenshot"}) {
		t.Errorf("the call of ios_tap %v", got)
	}

	tester := m.chats[0]
	if tester.opts.Browser != nil {
		t.Error("the browser toolset is offered without a browser")
	}
	for _, name := range []string{"ios_tree", "ios_tap", "ios_type", "ios_screenshot"} {
		if !slices.ContainsFunc(tester.opts.Tools, func(tl llm.Tool) bool { return tl.Name == name }) {
			t.Errorf("%s is not offered", name)
		}
	}
	if !strings.Contains(tester.opts.System, "The iOS app com.example.app on iPhone 17") || strings.Contains(tester.opts.System, "browser has a single tab") {
		t.Errorf("system prompt:\n%s", tester.opts.System)
	}
	if !strings.HasSuffix(tester.opts.Prompt, "The app com.example.app on iPhone 17 is open.") || !strings.Contains(tester.opts.Prompt, "seen in the app") {
		t.Errorf("prompt:\n%s", tester.opts.Prompt)
	}
	res := tester.turns[1].Results[0]
	if res.CallID != "1" || !strings.Contains(res.Text, `Typed "{secret:EMAIL}" into email.`) || !strings.Contains(res.Text, "Button, label: 'Sign in'") {
		t.Errorf("the result of ios_type %+v", res)
	}
	if strings.Contains(res.Text, "alice@example.com") {
		t.Error("the model was shown the value of a secret")
	}

	if len(o.Steps) != 2 || o.Steps[0].Target != "ios" || o.Steps[0].Input["value"] != "{secret:EMAIL}" || o.Steps[1].Screenshot != "step-02.png" {
		t.Errorf("steps %+v", o.Steps)
	}
	if len(o.Targets) != 1 || o.Targets[0] != (TargetState{Kind: "ios", App: "com.example.app", Device: "UDID-1", Screenshot: "final-ios.png"}) {
		t.Errorf("targets %+v", o.Targets)
	}
	if o.URL != "" || o.FinalScreenshot != "final-ios.png" || !slices.Contains(saved, "final-ios.png") {
		t.Errorf("url %q, final screenshot %q, saved %v", o.URL, o.FinalScreenshot, saved)
	}

	judge := m.chats[1]
	if !strings.Contains(judge.opts.Prompt, "<final_app_tree>") || !strings.Contains(judge.opts.Prompt, "A screenshot of the final screen of the app follows.") {
		t.Errorf("judge prompt:\n%s", judge.opts.Prompt)
	}
	if len(judge.opts.Images) != 1 || judge.opts.Images[0].MediaType != "image/png" {
		t.Errorf("judge images %+v", judge.opts.Images)
	}
}

func TestRun_IOSWithoutVision(t *testing.T) {
	ios := &fakeIOS{t: t, tree: "Application, label: 'Example'"}
	m := &fakeModel{blind: true, scripts: [][]llm.Reply{
		{
			toolCall("1", "ios_tap", map[string]any{"selector": "go"}),
			toolCall("2", "ios_screenshot", map[string]any{}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Done."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{"label: 'Example'"}})},
	}}
	cfg := config()
	o, err := Run(t.Context(), cfg, Targets{IOS: ios.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(m.chats[0].opts.Tools, func(tl llm.Tool) bool { return tl.Name == "ios_screenshot" }) {
		t.Error("ios_screenshot is offered to a model that sees no images")
	}
	if got := ios.names(1); !slices.Equal(got, []string{"tap", "tree"}) {
		t.Errorf("the call of ios_tap %v, want no screenshot", got)
	}
	if res := m.chats[0].turns[2].Results[0]; !res.IsError {
		t.Errorf("ios_screenshot ran for a model that sees no images: %+v", res)
	}
	if o.Steps[0].Screenshot != "" || len(m.chats[1].opts.Images) != 0 {
		t.Errorf("a screenshot was kept or shown: %+v", o.Steps[0])
	}
}

func TestRun_IOSActionThatFails(t *testing.T) {
	ios := &fakeIOS{t: t, tree: "Application", fail: "missing"}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "ios_tap", map[string]any{"selector": "missing"}),
			toolCall("2", "ios_tap", map[string]any{}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Done."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "fail", "reason": "r", "evidence": []string{}})},
	}}
	o, err := Run(t.Context(), config(), Targets{IOS: ios.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	turns := m.chats[0].turns
	if res := turns[1].Results[0]; !res.IsError || !strings.Contains(res.Text, "no element matches missing") {
		t.Errorf("the result of a tap that failed %+v", res)
	}
	if res := turns[2].Results[0]; !res.IsError || !strings.Contains(res.Text, "needs a selector") {
		t.Errorf("the result of a tap without a selector %+v", res)
	}
	if o.Steps[0].Error == "" || o.Steps[1].Error == "" {
		t.Errorf("steps %+v", o.Steps)
	}
}

func TestRun_IOSThatCannotOpen(t *testing.T) {
	m := &fakeModel{}
	app := &IOS{App: "com.example.app", Call: func(context.Context, []map[string]any) (map[string]any, error) {
		return nil, errors.New("no simulator is booted")
	}}
	_, err := Run(t.Context(), config(), Targets{IOS: app}, m)
	if err == nil || !strings.Contains(err.Error(), "opening the app com.example.app: no simulator is booted") {
		t.Fatalf("err %v", err)
	}
	if len(m.chats) != 0 {
		t.Error("the model was asked although the app did not open")
	}
}

// A test of a browser and an app tells the tester of both, and the judge
// of what each showed last.
func TestRun_BrowserAndIOS(t *testing.T) {
	p := &fakePage{title: "Account", read: `text "Your code is 4242"`, text: "Your code is 4242"}
	ios := &fakeIOS{t: t, tree: "Application, label: 'Signed in as Alice'"}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			browserCall("1", "get_page_text", map[string]any{}),
			toolCall("2", "ios_type", map[string]any{"selector": "code", "value": "4242"}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Signed in."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{"Signed in as Alice", "Your code is 4242"}})},
	}}
	o, err := Run(t.Context(), config(), Targets{Browser: p, IOS: ios.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass {
		t.Fatalf("verdict %s: %s", o.Verdict, o.Reason)
	}
	tester := m.chats[0]
	if tester.opts.Browser == nil || !slices.ContainsFunc(tester.opts.Tools, func(tl llm.Tool) bool { return tl.Name == "ios_tap" }) {
		t.Error("the tools of both targets are not offered")
	}
	for _, want := range []string{"The browser, through the browser toolset:", "Open only pages on these hosts: app.test:8080.", "The iOS app com.example.app on iPhone 17"} {
		if !strings.Contains(tester.opts.System, want) {
			t.Errorf("the system prompt lacks %q:\n%s", want, tester.opts.System)
		}
	}
	if !strings.HasSuffix(tester.opts.Prompt, "The browser has opened http://app.test:8080/. The app com.example.app on iPhone 17 is open.") || !strings.Contains(tester.opts.Prompt, "seen on the targets") {
		t.Errorf("prompt:\n%s", tester.opts.Prompt)
	}
	if o.Steps[0].Target != "browser" || o.Steps[1].Target != "ios" {
		t.Errorf("steps %+v", o.Steps)
	}
	if len(o.Targets) != 2 || o.Targets[0].Kind != "browser" || o.Targets[1].Kind != "ios" || o.URL == "" {
		t.Errorf("targets %+v, url %q", o.Targets, o.URL)
	}
	judge := m.chats[1].opts.Prompt
	if !strings.Contains(judge, "<final_page_text>") || !strings.Contains(judge, "<final_app_tree>") {
		t.Errorf("judge prompt:\n%s", judge)
	}
	for _, e := range o.Evidence {
		if !e.Verified {
			t.Errorf("evidence %q was not found in what the targets showed", e.Quote)
		}
	}
}

func TestRun_NoTarget(t *testing.T) {
	if _, err := Run(t.Context(), config(), Targets{}, &fakeModel{}); err == nil {
		t.Fatal("a test without a target ran")
	}
}
