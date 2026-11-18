package scenario

import (
	"slices"
	"strings"
	"testing"

	"github.com/mozership/probe-agent/internal/llm"
)

// An Android app is driven as an iOS app is, by tools of its own names,
// with back, and the selectors of probe-android.
func TestRun_DrivesAnAndroidApp(t *testing.T) {
	app := &fakeIOS{t: t, tree: `TextView id=android:id/title text="Network & internet" [200,540][700,600]
Switch id=android:id/switch_widget text="Airplane mode" [800,700][900,760]`}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "android_tap", map[string]any{"selector": `text="Network & internet"`}),
			toolCall("2", "android_back", map[string]any{}),
			toolCall("3", "android_launch", map[string]any{"args": []string{"-x"}}),
			toolCall("4", "ios_tap", map[string]any{"selector": "x"}),
			toolCall("5", "finish", map[string]any{"status": "done", "summary": "Opened."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{`text="Airplane mode"`}})},
	}}
	target := app.target()
	target.App, target.Device = "com.android.settings", ""
	cfg := config()
	cfg.StartURL = ""
	o, err := Run(t.Context(), cfg, Targets{Android: target}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass {
		t.Fatalf("verdict %s: %s", o.Verdict, o.Reason)
	}
	if got := app.names(0); !slices.Equal(got, []string{"activate", "tree"}) {
		t.Errorf("the opening %v", got)
	}
	if got := app.names(2); !slices.Equal(got, []string{"back", "tree", "screenshot"}) {
		t.Errorf("the call of android_back %v", got)
	}
	// probe-android's launch takes no arguments.
	if launch := app.calls[3][0]; launch["name"] != "launch" || launch["args"] != nil {
		t.Errorf("the launch %v", launch)
	}

	tester := m.chats[0]
	for _, name := range []string{"android_tree", "android_tap", "android_back", "android_screenshot"} {
		if !slices.ContainsFunc(tester.opts.Tools, func(tl llm.Tool) bool { return tl.Name == name }) {
			t.Errorf("%s is not offered", name)
		}
	}
	if slices.ContainsFunc(tester.opts.Tools, func(tl llm.Tool) bool { return strings.HasPrefix(tl.Name, "ios_") }) {
		t.Error("a tool of iOS is offered")
	}
	for _, want := range []string{"The Android app com.android.settings on the one device, through the tools that start with android_:", "android_back presses the back button", "class=Button text=\"Sign in\""} {
		if !strings.Contains(tester.opts.System, want) {
			t.Errorf("the system prompt lacks %q:\n%s", want, tester.opts.System)
		}
	}
	if res := tester.turns[4].Results[0]; !res.IsError {
		t.Errorf("a tool of iOS ran on Android: %+v", res)
	}
	if o.Steps[0].Target != "android" || o.Targets[0].Kind != "android" || o.Targets[0].Device != "UDID-1" {
		t.Errorf("steps %+v, targets %+v", o.Steps, o.Targets)
	}
	if judge := m.chats[1].opts.Prompt; !strings.Contains(judge, "<final_android_tree>") {
		t.Errorf("judge prompt:\n%s", judge)
	}
}

// An iOS app and an Android one are told apart in one test.
func TestRun_IOSAndAndroid(t *testing.T) {
	ios := &fakeIOS{t: t, tree: "Application, label: 'Code 4242'"}
	android := &fakeIOS{t: t, tree: `TextView text="Signed in"`}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "ios_tree", map[string]any{}),
			toolCall("2", "android_type", map[string]any{"selector": "code", "value": "4242"}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Done."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{"Code 4242", `text="Signed in"`}})},
	}}
	cfg := config()
	cfg.StartURL = ""
	o, err := Run(t.Context(), cfg, Targets{IOS: ios.target(), Android: android.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	// Each is opened, called once, and read at the end.
	if len(ios.calls) != 3 || len(android.calls) != 3 || android.calls[1][0]["value"] != "4242" {
		t.Errorf("ios %v, android %v", ios.calls, android.calls)
	}
	if o.Steps[0].Target != "ios" || o.Steps[1].Target != "android" {
		t.Errorf("steps %+v", o.Steps)
	}
	judge := m.chats[1].opts.Prompt
	if !strings.Contains(judge, "<final_ios_tree>") || !strings.Contains(judge, "<final_android_tree>") {
		t.Errorf("judge prompt:\n%s", judge)
	}
}
