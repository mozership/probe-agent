package scenario

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/mozership/probe-agent/internal/llm"
)

// signInTree is the screen of an app after a sign-in.
const signInTree = `Application, label: 'Example'
  TextField, identifier: 'email'
  Button, label: 'Sign in'
  StaticText, label: 'Welcome back, Alice!'`

// appConfig is a test of an app that signs in.
func appConfig() Config {
	cfg := config()
	cfg.StartURL = ""
	cfg.Task = "Sign in with {secret:EMAIL}."
	cfg.Expect = "The app welcomes Alice back."
	return cfg
}

// recordedApp runs the test of the app live with Record, and returns its
// recording.
func recordedApp(t *testing.T) *Recording {
	t.Helper()
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "ios_tree", map[string]any{}),
			toolCall("2", "ios_type", map[string]any{"selector": "email", "value": "{secret:EMAIL}"}),
			toolCall("3", "ios_tap", map[string]any{"selector": `type=button label="Sign in"`}),
			toolCall("4", "finish", map[string]any{"status": "done", "summary": "Signed in."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{"Welcome back, Alice!"}})},
	}}
	cfg := appConfig()
	cfg.Record = true
	ios := &fakeIOS{t: t, tree: signInTree}
	o, err := Run(t.Context(), cfg, Targets{IOS: ios.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass || o.Recording == nil {
		t.Fatalf("verdict %s (%s), recording %v", o.Verdict, o.Reason, o.Recording)
	}
	return o.Recording
}

func TestRun_RecordsTheActionsOfAnApp(t *testing.T) {
	rec := recordedApp(t)
	if rec.Start != "" || len(rec.Steps) != 3 {
		t.Fatalf("recording %+v", rec)
	}
	tree, typed, tap := rec.Steps[0], rec.Steps[1], rec.Steps[2]
	if tree.Target != "ios" || tree.Tool != "ios_tree" || tree.Outcome != "ok" {
		t.Errorf("the tree %+v", tree)
	}
	if typed.Input["value"] != "{secret:EMAIL}" || typed.Gap != "" || tap.Input["selector"] != `type=button label="Sign in"` {
		t.Errorf("the type %+v, the tap %+v", typed, tap)
	}
	b, _ := json.Marshal(rec)
	if strings.Contains(string(b), "alice@example.com") {
		t.Errorf("the recording holds a secret: %s", b)
	}
	if rec.Checks == nil || !slices.Equal(rec.Checks.Quotes, []string{"Welcome back, Alice!"}) {
		t.Errorf("checks %+v", rec.Checks)
	}
}

func TestRun_ReplaysTheActionsOfAnAppWithoutTheModel(t *testing.T) {
	rec := recordedApp(t)
	ios := &fakeIOS{t: t, tree: signInTree}
	m := &fakeModel{}
	cfg := appConfig()
	cfg.Recording = rec
	o, err := Run(t.Context(), cfg, Targets{IOS: ios.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.chats) != 0 || o.Verdict != Pass || o.Replay.Status != ReplayHit || o.Replay.Replayed != 3 || !o.Replay.Checked {
		t.Fatalf("asked %d times, verdict %s (%s), replay %+v", len(m.chats), o.Verdict, o.Reason, o.Replay)
	}
	typed := false
	for _, call := range ios.calls {
		if call[0]["name"] == "type" {
			typed = call[0]["value"] == "alice@example.com" && call[0]["selector"] == "email"
		}
	}
	if !typed {
		t.Errorf("the secret was not typed again: %v", ios.calls)
	}
}

func TestRun_AnElementTheAppNoLongerHasHandsTheTestOver(t *testing.T) {
	rec := recordedApp(t)
	ios := &fakeIOS{t: t, tree: signInTree, fail: `type=button label="Sign in"`}
	m := &fakeModel{scripts: [][]llm.Reply{
		{toolCall("1", "finish", map[string]any{"status": "done", "summary": "Signed in."})},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{"Welcome back, Alice!"}})},
	}}
	cfg := appConfig()
	cfg.Recording = rec
	o, err := Run(t.Context(), cfg, Targets{IOS: ios.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Replay.Status != ReplayPartial || o.Replay.Replayed != 2 || o.Replay.DivergedAt != 3 || !o.Replay.Stale || !strings.Contains(o.Replay.Reason, "ios_tap failed") {
		t.Fatalf("replay %+v", o.Replay)
	}
	prompt := m.chats[0].opts.Prompt
	for _, want := range []string{"2. ios_type of ios", "from the screen the app is on; read it first with ios_tree"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the tester's prompt lacks %q:\n%s", want, prompt)
		}
	}
}

func TestRun_ACodeTheAppShowedIsTypedAgainFromTheScreenOfTheReplay(t *testing.T) {
	codeTree := func(code string) string {
		return "Application, label: 'Example'\n  StaticText, label: 'Code: " + code + "'\n  TextField, identifier: 'code'"
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "android_tree", map[string]any{}),
			toolCall("2", "android_type", map[string]any{"selector": "code", "value": "482913"}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Typed the code."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{"Example"}})},
	}}
	cfg := appConfig()
	cfg.Task, cfg.Expect = "Type the code the app shows.", "The app takes the code."
	cfg.Record = true
	o, err := Run(t.Context(), cfg, Targets{Android: (&fakeIOS{t: t, tree: codeTree("482913")}).target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Recording == nil || len(o.Recording.Steps) != 2 {
		t.Fatalf("verdict %s (%s), recording %+v", o.Verdict, o.Reason, o.Recording)
	}
	typed := o.Recording.Steps[1]
	if typed.Target != "android" || typed.Input["value"] != "{answer:1}" || len(typed.Refs) != 1 || typed.Refs[0].Step != 1 || typed.Refs[0].Key != "'Code: " {
		t.Fatalf("the type %+v", typed)
	}

	app := &fakeIOS{t: t, tree: codeTree("771045")}
	cfg.Recording, cfg.Record = o.Recording, false
	o, err = Run(t.Context(), cfg, Targets{Android: app.target()}, &fakeModel{})
	if err != nil {
		t.Fatal(err)
	}
	if o.Replay.Status != ReplayHit {
		t.Fatalf("replay %+v", o.Replay)
	}
	got := ""
	for _, call := range app.calls {
		if call[0]["name"] == "type" {
			got, _ = call[0]["value"].(string)
		}
	}
	if got != "771045" {
		t.Errorf("typed %q, want the code of the replay", got)
	}
}

func TestRun_AValueTypedAfterAScreenshotIsAGap(t *testing.T) {
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "ios_screenshot", map[string]any{}),
			toolCall("2", "ios_type", map[string]any{"selector": "code", "value": "QX7-PLM"}),
			toolCall("3", "ios_type", map[string]any{"selector": "email", "value": "{secret:EMAIL}"}),
			toolCall("4", "finish", map[string]any{"status": "done", "summary": "Typed."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{"Welcome back, Alice!"}})},
	}}
	cfg := appConfig()
	cfg.Record = true
	o, err := Run(t.Context(), cfg, Targets{IOS: (&fakeIOS{t: t, tree: signInTree}).target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Recording == nil {
		t.Fatalf("verdict %s (%s), no recording", o.Verdict, o.Reason)
	}
	// The screenshot is not a step; the value typed after it is a gap,
	// and the secret typed after another move is not.
	steps := o.Recording.Steps
	if len(steps) != 2 || steps[0].Gap == "" || steps[0].Input != nil || steps[1].Gap != "" {
		t.Fatalf("steps %+v", steps)
	}
	b, _ := json.Marshal(o.Recording)
	if strings.Contains(string(b), "QX7-PLM") {
		t.Errorf("the recording holds the value of the gap: %s", b)
	}
}
