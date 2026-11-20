package scenario

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mozership/probe-agent/internal/llm"
	"github.com/mozership/probe-agent/internal/page"
)

var (
	signUpLink = page.Locator{Role: "link", Name: "Sign up"}
	emailField = page.Locator{Role: "textbox", Name: "Email"}
	pwField    = page.Locator{Role: "textbox", Name: "Password"}
)

func judgePass(quote string) []llm.Reply {
	return []llm.Reply{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "Greeted.", "evidence": []string{quote}})}
}

// recorded runs the scenario test live with Record, and returns its
// recording.
func recorded(t *testing.T) *Recording {
	t.Helper()
	p := &fakePage{
		url:        "http://app.test:8080/",
		afterClick: "http://app.test:8080/signup",
		text:       "Welcome, alice@example.com!",
		locators:   map[string]page.Locator{"ref_1": signUpLink, "ref_2": emailField, "ref_3": pwField},
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			browserCall("1", "read_page", map[string]any{}),
			browserCall("2", "left_click", map[string]any{"target": map[string]any{"type": "ref", "ref": "ref_1"}}),
			browserCall("3", "form_input", map[string]any{"target": map[string]any{"type": "ref", "ref": "ref_2"}, "value": "{secret:EMAIL}"}),
			browserCall("4", "left_click", map[string]any{"target": map[string]any{"type": "ref", "ref": "ref_3"}}),
			browserCall("5", "type", map[string]any{"text": "{secret:PASSWORD}"}),
			browserCall("6", "navigate", map[string]any{"url": "http://app.test:8080/welcome"}),
			toolCall("7", "finish", map[string]any{"status": "done", "summary": "Welcome."}),
		},
		judgePass("Welcome, {secret:EMAIL}!"),
	}}
	cfg := config()
	cfg.Record = true
	o, err := Run(t.Context(), cfg, Targets{Browser: p}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass || o.Recording == nil {
		t.Fatalf("verdict %s (%s), recording %v", o.Verdict, o.Reason, o.Recording)
	}
	return o.Recording
}

func TestRun_RecordsAPass(t *testing.T) {
	rec := recorded(t)
	if rec.Version != recordingVersion || rec.Start != "/" || rec.Landing != "/" || !strings.Contains(rec.Task, "{secret:EMAIL}") {
		t.Errorf("recording %+v", rec)
	}
	var tools []string
	for _, s := range rec.Steps {
		tools = append(tools, s.Tool)
	}
	if !slices.Equal(tools, []string{"left_click", "form_input", "left_click", "type", "navigate"}) {
		t.Fatalf("recorded %v; reads are not recorded", tools)
	}
	if s := rec.Steps[0]; s.Locator == nil || *s.Locator != signUpLink || s.Path != "/signup" {
		t.Errorf("the click %+v", s)
	}
	if _, ok := rec.Steps[1].Input["target"]; ok || rec.Steps[1].Input["value"] != "{secret:EMAIL}" {
		t.Errorf("the input of a step with a locator %v; want its value and no target", rec.Steps[1].Input)
	}
	if s := rec.Steps[4]; s.Input["url"] != "/welcome" {
		t.Errorf("the navigation on the host of the test is kept as a path: %+v", s)
	}
	b, _ := json.Marshal(rec)
	if strings.Contains(string(b), "alice@") || strings.Contains(string(b), "hunter2") {
		t.Errorf("the recording holds a secret: %s", b)
	}
}

// stagingPage is another run of the recorded test, on another host, where
// the elements have other refs.
func stagingPage() *fakePage {
	return &fakePage{
		url:        "http://staging.test/",
		afterClick: "http://staging.test/signup",
		text:       "Welcome, alice@example.com!",
		locators:   map[string]page.Locator{"ref_7": signUpLink, "ref_8": emailField, "ref_9": pwField},
	}
}

// replayed runs the recording on p, recording the run again if it changes.
func replayed(t *testing.T, rec *Recording, p *fakePage, m *fakeModel, rejudge bool) Outcome {
	t.Helper()
	cfg := config()
	cfg.StartURL = "http://staging.test/"
	cfg.Recording, cfg.Record, cfg.Rejudge = rec, true, rejudge
	o, err := Run(t.Context(), cfg, Targets{Browser: p}, m)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestRun_ReplaysAllWithoutTheModel(t *testing.T) {
	rec := recorded(t)
	p := stagingPage()
	m := &fakeModel{}
	o := replayed(t, rec, p, m, false)
	if len(m.chats) != 0 || o.Usage.Requests != 0 {
		t.Fatalf("the model was asked %d times; want none", len(m.chats))
	}
	if o.Verdict != Pass || o.Replay == nil || o.Replay.Status != ReplayHit || o.Replay.Replayed != 5 || !o.Replay.Checked {
		t.Errorf("verdict %s, replay %+v", o.Verdict, o.Replay)
	}
	if len(o.Evidence) != 1 || o.Evidence[0] != (Evidence{Quote: "Welcome, {secret:EMAIL}!", Verified: true}) || !strings.Contains(o.Reason, "the model was not asked to judge") {
		t.Errorf("reason %q, evidence %+v", o.Reason, o.Evidence)
	}
	if o.Recording != nil {
		t.Errorf("a full replay was recorded again")
	}
	if !slices.Equal(p.typed, []string{"hunter2-long"}) || len(p.inputs) != 1 || p.inputs[0] != "alice@example.com" {
		t.Errorf("typed %v, inputs %v; want the secrets filled in", p.typed, p.inputs)
	}
	if last := p.navigated[len(p.navigated)-1]; last != "http://staging.test/welcome" {
		t.Errorf("navigated to %s; want the path on the host of the run", last)
	}
	if len(o.Steps) != 5 || !strings.Contains(o.Steps[0].Notes[0], "Replayed step 1") {
		t.Errorf("steps %+v", o.Steps)
	}
}

func TestRun_RecordsWhatTheJudgeQuotedOfTheLastPage(t *testing.T) {
	rec := recorded(t)
	if rec.Checks == nil || !slices.Equal(rec.Checks.Quotes, []string{"Welcome, {secret:EMAIL}!"}) || len(rec.Checks.Errors) != 0 {
		t.Errorf("checks %+v", rec.Checks)
	}
}

func TestRun_TheJudgeJudgesAReplayThatMissesTheChecks(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*fakePage)
		why    string
	}{
		"another text": {func(p *fakePage) { p.text = "Welcome back, alice@example.com!" }, `does not show "Welcome, {secret:EMAIL}!"`},
		"a new error": {func(p *fakePage) {
			p.network = []page.NetworkError{{Method: "GET", URL: "http://staging.test/api/me/42", Status: 500}}
		}, "the page had an error the recorded run did not: GET /api/me/:id 500"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := recorded(t)
			p := stagingPage()
			tc.change(p)
			quoted := "Welcome"
			m := &fakeModel{scripts: [][]llm.Reply{judgePass(quoted)}}
			o := replayed(t, rec, p, m, false)
			if len(m.chats) != 1 || !strings.HasPrefix(m.chats[0].opts.System, judgeSystem) {
				t.Fatalf("the model was asked %d times; want the judge", len(m.chats))
			}
			if o.Verdict != Pass || o.Replay.Checked || !strings.Contains(o.Replay.Reason, tc.why) {
				t.Errorf("verdict %s, replay %+v", o.Verdict, o.Replay)
			}
			// The judge passed it again, on what it quoted this time.
			if o.Recording == nil || !slices.Equal(o.Recording.Checks.Quotes, []string{quoted}) || len(o.Recording.Steps) != len(rec.Steps) {
				t.Fatalf("recording %+v", o.Recording)
			}
			if name == "a new error" && !slices.Equal(o.Recording.Checks.Errors, []string{"GET /api/me/:id 500"}) {
				t.Errorf("errors %v", o.Recording.Checks.Errors)
			}
		})
	}
}

func TestRun_AReplayOfARecordingWithoutChecksIsJudged(t *testing.T) {
	rec := recorded(t)
	rec.Checks = nil
	m := &fakeModel{scripts: [][]llm.Reply{judgePass("Welcome, {secret:EMAIL}!")}}
	o := replayed(t, rec, stagingPage(), m, false)
	if len(m.chats) != 1 || o.Verdict != Pass || o.Replay.Checked || !strings.Contains(o.Replay.Reason, "no checks") {
		t.Fatalf("asked %d times, verdict %s, replay %+v", len(m.chats), o.Verdict, o.Replay)
	}
	// It is recorded with the checks the judge passed it on, and the steps
	// it had.
	if o.Recording == nil || o.Recording.Checks == nil || !slices.Equal(o.Recording.Checks.Quotes, []string{"Welcome, {secret:EMAIL}!"}) {
		t.Fatalf("recording %+v", o.Recording)
	}
	o.Recording.Checks = nil
	if !sameRecording(o.Recording, rec) {
		t.Errorf("the steps changed: %+v", o.Recording)
	}
}

func TestRun_RejudgeHasTheJudgeJudgeAFullReplay(t *testing.T) {
	rec := recorded(t)
	m := &fakeModel{scripts: [][]llm.Reply{judgePass("Welcome, {secret:EMAIL}!")}}
	o := replayed(t, rec, stagingPage(), m, true)
	if len(m.chats) != 1 || o.Verdict != Pass || o.Replay.Checked || o.Recording != nil {
		t.Errorf("asked %d times, verdict %s, replay %+v, recording %v", len(m.chats), o.Verdict, o.Replay, o.Recording)
	}
}

func TestRun_AJudgeThatFailsAFullReplay(t *testing.T) {
	rec := recorded(t)
	p := stagingPage()
	p.text = "Something went wrong"
	m := &fakeModel{scripts: [][]llm.Reply{{toolCall("j1", "verdict", map[string]any{"verdict": "fail", "reason": "No welcome.", "evidence": []string{"Something went wrong"}})}}}
	o := replayed(t, rec, p, m, false)
	if o.Verdict != Fail || o.Recording != nil {
		t.Errorf("verdict %s, recording %v", o.Verdict, o.Recording)
	}
}

func TestRun_HandsOffWhereTheRecordingStopsMatching(t *testing.T) {
	rec := recorded(t)
	// The password field is now labelled otherwise.
	newPw := page.Locator{Role: "textbox", Name: "Choose a password"}
	p := &fakePage{
		url:        "http://app.test:8080/",
		afterClick: "http://app.test:8080/signup",
		text:       "Welcome, alice@example.com!",
		locators:   map[string]page.Locator{"ref_1": signUpLink, "ref_2": emailField, "ref_3": newPw},
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			browserCall("1", "left_click", map[string]any{"target": map[string]any{"type": "ref", "ref": "ref_3"}}),
			browserCall("2", "type", map[string]any{"text": "{secret:PASSWORD}"}),
			browserCall("3", "navigate", map[string]any{"url": "http://app.test:8080/welcome"}),
			toolCall("4", "finish", map[string]any{"status": "done", "summary": "Welcome."}),
		},
		judgePass("Welcome, {secret:EMAIL}!"),
	}}
	cfg := config()
	cfg.Recording, cfg.Record = rec, true
	o, err := Run(t.Context(), cfg, Targets{Browser: p}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Replay.Status != ReplayPartial || o.Replay.Replayed != 2 || o.Replay.DivergedAt != 3 || !strings.Contains(o.Replay.Reason, `textbox "Password"`) {
		t.Errorf("replay %+v", o.Replay)
	}
	prompt := m.chats[0].opts.Prompt
	for _, want := range []string{"The first 2 steps of the task were replayed", `1. left_click on link "Sign up"`, `value "{secret:EMAIL}"`, `could not be replayed: the page has no textbox "Password"`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the tester's prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "alice@") {
		t.Errorf("the tester saw a secret")
	}
	if o.Verdict != Pass || o.Recording == nil || len(o.Recording.Steps) != 5 {
		t.Fatalf("verdict %s, recording %+v", o.Verdict, o.Recording)
	}
	if l := o.Recording.Steps[2].Locator; l == nil || *l != newPw {
		t.Errorf("the new recording clicks %+v; want the new field", l)
	}
}

func TestRun_ReplayChecksWhereStepsLead(t *testing.T) {
	defer func(d time.Duration) { pathWait = d }(pathWait)
	pathWait = 50 * time.Millisecond

	rec := recorded(t)
	p := &fakePage{
		url:        "http://app.test:8080/",
		afterClick: "http://app.test:8080/maintenance",
		locators:   map[string]page.Locator{"ref_1": signUpLink},
		text:       "Down for maintenance",
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		{toolCall("1", "finish", map[string]any{"status": "done", "summary": "Down."})},
		{toolCall("j1", "verdict", map[string]any{"verdict": "fail", "reason": "Down.", "evidence": []string{}})},
	}}
	cfg := config()
	cfg.Recording, cfg.Record = rec, true
	o, err := Run(t.Context(), cfg, Targets{Browser: p}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Replay.Status != ReplayPartial || o.Replay.DivergedAt != 1 || !strings.Contains(o.Replay.Reason, "went to /maintenance, where the recording went to /signup") {
		t.Errorf("replay %+v", o.Replay)
	}
	if o.Verdict != Fail || o.Recording != nil {
		t.Errorf("verdict %s; a failed test was recorded: %v", o.Verdict, o.Recording != nil)
	}
}

func TestRun_ReplayOnlyForTheSameTask(t *testing.T) {
	rec := recorded(t)
	p := &fakePage{url: "http://app.test:8080/", text: "Welcome"}
	m := &fakeModel{scripts: [][]llm.Reply{
		{toolCall("1", "finish", map[string]any{"status": "done", "summary": "Welcome."})},
		judgePass("Welcome"),
	}}
	cfg := config()
	cfg.Task = "Sign up with a Google account."
	cfg.Recording = rec
	o, err := Run(t.Context(), cfg, Targets{Browser: p}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Replay.Status != ReplayMiss || !strings.Contains(o.Replay.Reason, "task or the expectation changed") || len(m.chats) != 2 {
		t.Errorf("replay %+v, chats %d", o.Replay, len(m.chats))
	}
	if o.Recording != nil {
		t.Errorf("recorded without Record")
	}
}

func TestSameRoute(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"/orders/42", "/orders/7", true},
		{"/orders/42?t=1727780000", "/orders/7?t=1727780999", true},
		{"/notes/3f2b9c1e-8a6d-4e5f-9b1a-2c3d4e5f6a7b", "/notes/0b1c2d3e-4f5a-6b7c-8d9e-0f1a2b3c4d5e", true},
		{"/invoices/INV-2041", "/invoices/INV-3007", true},
		{"/report?from=2026-10-01", "/report?from=2026-10-23", true},
		{"/a?x=1&y=2", "/a?y=2&x=1", true},
		{"/welcome#top", "/welcome", true},
		{"/#/notes/1", "/#/notes/2", true},
		{"/", "/", true},
		{"/companies", "/companies?tab=notes", false},
		{"/mode?m=safe", "/mode?m=unsafe", false},
		{"/products/summer-sneaker", "/products/winter-boot", false},
		{"/page-2", "/page-3", false},
		{"/signup", "/welcome", false},
		{"/#/notes", "/#/settings", false},
		{"https://auth.test/login", "/login", false},
	} {
		if got := sameRoute(c.a, c.b); got != c.same {
			t.Errorf("sameRoute(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestDerived(t *testing.T) {
	shown := []string{"Your code is 482913", "Order ORD-7781 placed", "Alice Tester", "Row 1"}
	task := "Sign up as Alice Tester with {secret:EMAIL}, and write a note that says Buy milk 2 times."
	for _, c := range []struct {
		value  string
		pixels bool
		want   string
	}{
		{"Alice Tester", false, ""},
		{"{secret:EMAIL}", true, ""},
		{"Buy milk", false, ""},
		{"Hello there", false, ""},
		{"482913", false, derivedToken},
		{"ORD-7781", false, derivedToken},
		{"Order ORD-7781 placed", false, derivedWholeNode},
		{"2026-10-24", false, derivedDate},
		{"Oct 24", false, derivedDate},
		{"Hello there", true, derivedPixels},
		{"n4829", false, ""},
	} {
		if got := derived(c.value, task, shown, c.pixels); got != c.want {
			t.Errorf("derived(%q, pixels %v) = %q; want %q", c.value, c.pixels, got, c.want)
		}
	}
	if got := shownTexts("heading \"Your code\" level=1 [ref_1]\n  text \"is 482913\"\nPlain line"); len(got) != 3 || got[1] != "is 482913" || got[2] != "Plain line" {
		t.Errorf("shownTexts = %q", got)
	}
}

// codeRecording records a sign-up whose last step types a code the page
// showed, which the next run's page shows another of.
func codeRecording(t *testing.T) *Recording {
	t.Helper()
	p := &fakePage{
		url:      "http://app.test:8080/",
		read:     `text "Your code is 482913"` + "\n" + `textbox "Code" [ref_2]`,
		text:     "Welcome, alice@example.com!",
		locators: map[string]page.Locator{"ref_1": signUpLink, "ref_2": {Role: "textbox", Name: "Code"}},
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			browserCall("1", "left_click", map[string]any{"target": map[string]any{"type": "ref", "ref": "ref_1"}}),
			browserCall("2", "read_page", map[string]any{}),
			browserCall("3", "form_input", map[string]any{"target": map[string]any{"type": "ref", "ref": "ref_2"}, "value": "482913"}),
			toolCall("4", "finish", map[string]any{"status": "done", "summary": "Welcome."}),
		},
		judgePass("Welcome, {secret:EMAIL}!"),
	}}
	cfg := config()
	cfg.Record = true
	o, err := Run(t.Context(), cfg, Targets{Browser: p}, m)
	if err != nil || o.Recording == nil {
		t.Fatalf("recording %v, %v", o.Recording, err)
	}
	return o.Recording
}

func TestRun_ValuesReadOffTheScreenAreGaps(t *testing.T) {
	rec := codeRecording(t)
	if g := rec.Steps[1].Gap; !strings.Contains(g, derivedToken) || rec.Steps[1].Input["value"] != nil {
		t.Fatalf("the typed code is no gap: %+v", rec.Steps[1])
	}

	// The replay clicks, then hands over at the code, even in strict mode,
	// as the application did not change.
	p := &fakePage{
		url:      "http://app.test:8080/",
		read:     `text "Your code is 771204"` + "\n" + `textbox "Code" [ref_5]`,
		text:     "Welcome, alice@example.com!",
		locators: map[string]page.Locator{"ref_4": signUpLink, "ref_5": {Role: "textbox", Name: "Code"}},
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			browserCall("1", "read_page", map[string]any{}),
			browserCall("2", "form_input", map[string]any{"target": map[string]any{"type": "ref", "ref": "ref_5"}, "value": "771204"}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Welcome."}),
		},
		judgePass("Welcome, {secret:EMAIL}!"),
	}}
	cfg := config()
	cfg.Recording, cfg.Record, cfg.Strict = rec, true, true
	o, err := Run(t.Context(), cfg, Targets{Browser: p}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Replay.Status != ReplayPartial || o.Replay.DivergedAt != 2 || o.Replay.Stale || o.Verdict != Pass {
		t.Errorf("replay %+v, verdict %s", o.Replay, o.Verdict)
	}
	if strings.Contains(m.chats[0].opts.Prompt, "may have changed") {
		t.Errorf("a gap was told as a change of the application")
	}
	// The run recorded is the same, gap and all, so it is not written.
	if o.Recording != nil {
		t.Errorf("an unchanged recording is written again: %+v", o.Recording.Steps)
	}
}

func TestRun_StrictStopsAStaleReplay(t *testing.T) {
	rec := recorded(t)
	p := &fakePage{url: "http://app.test:8080/", locators: map[string]page.Locator{}}
	m := &fakeModel{}
	cfg := config()
	cfg.Recording, cfg.Strict = rec, true
	o, err := Run(t.Context(), cfg, Targets{Browser: p}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Blocked || o.Stopped != StoppedStale || !strings.Contains(o.Reason, `link "Sign up"`) || len(m.chats) != 0 {
		t.Errorf("verdict %s, stopped %s (%s), chats %d", o.Verdict, o.Stopped, o.Reason, len(m.chats))
	}
}

func TestRun_ReplayStopsAtAnAlertItDidNotSee(t *testing.T) {
	rec := recorded(t)
	p := &fakePage{
		url:        "http://app.test:8080/",
		afterClick: "http://app.test:8080/signup",
		locators:   map[string]page.Locator{"ref_1": signUpLink, "ref_2": emailField, "ref_3": pwField},
		alerts:     []string{"Sign-ups are closed until 10:00"},
	}
	m := &fakeModel{}
	cfg := config()
	cfg.Recording, cfg.Strict = rec, true
	o, err := Run(t.Context(), cfg, Targets{Browser: p}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Replay.DivergedAt != 1 || !strings.Contains(o.Replay.Reason, `alert the recording did not: "Sign-ups are closed until #:#"`) {
		t.Errorf("replay %+v", o.Replay)
	}
}

func TestRun_ReplayBeginsOnlyWhereTheRecordingBegan(t *testing.T) {
	rec := recorded(t)
	p := &fakePage{url: "http://app.test:8080/", landOn: "http://app.test:8080/login", locators: map[string]page.Locator{"ref_1": signUpLink}}
	m := &fakeModel{}
	cfg := config()
	cfg.Recording, cfg.Strict = rec, true
	o, err := Run(t.Context(), cfg, Targets{Browser: p}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Replay.Status != ReplayMiss || !o.Replay.Stale || !strings.Contains(o.Replay.Reason, "began on /login, where the recording began on /") || o.Stopped != StoppedStale {
		t.Errorf("replay %+v, stopped %s", o.Replay, o.Stopped)
	}
}

func TestRun_PointStepsNeedTheSameViewport(t *testing.T) {
	p := &fakePage{url: "http://app.test:8080/", text: "Welcome"}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			browserCall("1", "left_click", map[string]any{"target": map[string]any{"type": "coordinate", "x": 40, "y": 700}}),
			toolCall("2", "finish", map[string]any{"status": "done", "summary": "Welcome."}),
		},
		judgePass("Welcome"),
	}}
	cfg := config()
	cfg.Record, cfg.Viewport = true, Viewport{Width: 1280, Height: 800}
	o, err := Run(t.Context(), cfg, Targets{Browser: p}, m)
	if err != nil || o.Recording == nil || o.Recording.Steps[0].Gap != "" {
		t.Fatalf("recording %+v, %v", o.Recording, err)
	}

	cfg.Recording, cfg.Strict, cfg.Viewport = o.Recording, true, Viewport{Width: 390, Height: 844}
	o, err = Run(t.Context(), cfg, Targets{Browser: &fakePage{url: "http://app.test:8080/"}}, &fakeModel{})
	if err != nil {
		t.Fatal(err)
	}
	if o.Replay.DivergedAt != 1 || !strings.Contains(o.Replay.Reason, "viewport of 1280x800, and this one is 390x844") {
		t.Errorf("replay %+v", o.Replay)
	}
}

func TestErrorKinds_HideWhatChangesAndTheSecrets(t *testing.T) {
	cfg := config()
	cfg.StartURL = "http://staging.test/"
	d := &browserDriver{ledger: &ledger{cfg: cfg, secrets: newSecrets(cfg.Secrets)}, page: &fakePage{network: []page.NetworkError{
		{Method: "GET", URL: "http://staging.test/api/users?email=alice%40example.com", Status: 404},
	}}}
	d.page.(*fakePage).console = []page.ConsoleError{{Type: "error", Text: "Request 3f2a9c1d-0b4e-4c2a-9f1e-2d3c4b5a6e7f to http://staging.test/api failed at line 12"}}
	got := d.errorKinds()
	want := []string{"GET /api/users?email={secret:EMAIL} 404", "error: Request # to /api failed at line #"}
	if !slices.Equal(got, want) {
		t.Errorf("error kinds %q, want %q", got, want)
	}
}

func TestRun_AQuoteOfAFieldMakesNoCheck(t *testing.T) {
	rec := recorded(t)
	p := stagingPage()
	// The page stays on the form, whose field holds the email.
	p.text, p.read = "Something went wrong", `textbox "Email" value="alice@example.com"`
	rec.Checks = &Checks{Quotes: []string{"{secret:EMAIL}"}}
	m := &fakeModel{scripts: [][]llm.Reply{{toolCall("j1", "verdict", map[string]any{"verdict": "fail", "reason": "No welcome.", "evidence": []string{"Something went wrong"}})}}}
	o := replayed(t, rec, p, m, false)
	if len(m.chats) != 1 || o.Verdict != Fail || o.Replay.Checked {
		t.Errorf("asked %d times, verdict %s, replay %+v", len(m.chats), o.Verdict, o.Replay)
	}
}

func TestRun_RejudgeDoesNotRecordTheChecksAgain(t *testing.T) {
	rec := recorded(t)
	p := stagingPage()
	p.text = "Welcome back, alice@example.com!"
	m := &fakeModel{scripts: [][]llm.Reply{judgePass("Welcome back")}}
	if o := replayed(t, rec, p, m, true); o.Verdict != Pass || o.Recording != nil {
		t.Errorf("verdict %s, recording %+v", o.Verdict, o.Recording)
	}
}
