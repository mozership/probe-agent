package action

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mozership/probe/actionref"

	"github.com/mozership/probe-agent/internal/llm"
	"github.com/mozership/probe-agent/internal/page"
	"github.com/mozership/probe-agent/internal/scenario"
)

// TestManifestsDeclare checks that action.yml, and the one the e2e workflows
// use, declare the params the action takes, so that probe check takes it as
// it is, and declare no guard, as the action keeps to none.
func TestManifestsDeclare(t *testing.T) {
	for _, name := range []string{"action.yml", "e2e/action/action.yml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		m, err := actionref.ParseManifest(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(m.Params, Params()) {
			t.Errorf("%s declares params %v, want %v", name, m.Params, Params())
		}
		if m.Guard != nil {
			t.Errorf("%s declares guard %v, want none", name, m.Guard)
		}
	}
}

func TestParse_Defaults(t *testing.T) {
	r, err := parse(map[string]any{"task": " Sign up ", "expect": "An account", "url": "http://localhost:8080/"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Task != "Sign up" || r.Model != llm.DefaultModel || r.Effort != "medium" || r.MaxSteps != 40 ||
		r.Timeout != 4*time.Minute || !r.Headless || r.Width != 1280 || r.Height != 800 {
		t.Errorf("defaults %+v", r)
	}
}

func TestParse_TakesTemplatedStrings(t *testing.T) {
	r, err := parse(map[string]any{
		"task": "t", "expect": "e", "url": "https://app.test/",
		"max_steps":   "25",
		"timeout":     "90s",
		"headless":    "false",
		"window_w":    1024.0,
		"allow_hosts": "app.test, auth.test",
		"secrets":     map[string]any{"PASSWORD": "pw", "PIN": 1234.0},
		"effort":      "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.MaxSteps != 25 || r.Timeout != 90*time.Second || r.Headless || r.Width != 1024 || r.Effort != "high" {
		t.Errorf("parsed %+v", r)
	}
	if !slices.Equal(r.AllowHosts, []string{"app.test", "auth.test"}) {
		t.Errorf("allow_hosts %v", r.AllowHosts)
	}
	if r.Secrets["PASSWORD"] != "pw" || r.Secrets["PIN"] != "1234" {
		t.Errorf("secrets %v", r.Secrets)
	}
}

func TestParse_Refuses(t *testing.T) {
	for name, with := range map[string]map[string]any{
		"no task":          {"expect": "e", "url": "http://a.test/"},
		"no expect":        {"task": "t", "url": "http://a.test/"},
		"no url":           {"task": "t", "expect": "e"},
		"a url not http":   {"task": "t", "expect": "e", "url": "file:///etc/passwd"},
		"a bad timeout":    {"task": "t", "expect": "e", "url": "http://a.test/", "timeout": "soon"},
		"zero steps":       {"task": "t", "expect": "e", "url": "http://a.test/", "max_steps": 0.0},
		"a bad effort":     {"task": "t", "expect": "e", "url": "http://a.test/", "effort": "huge"},
		"a bad secret":     {"task": "t", "expect": "e", "url": "http://a.test/", "secrets": map[string]any{"MY-PW": "x"}},
		"an unknown key":   {"task": "t", "expect": "e", "url": "http://a.test/", "prompt": "x"},
		"both modes":       {"task": "t", "expect": "e", "persona": "p", "goal": "g", "url": "http://a.test/"},
		"no goal":          {"persona": "p", "url": "http://a.test/"},
		"only a goal":      {"goal": "g", "url": "http://a.test/"},
		"a bad cache mode": {"task": "t", "expect": "e", "url": "http://a.test/", "cache": "c.json", "cache_mode": "sometimes"},
		"a persona cache":  {"persona": "p", "goal": "g", "url": "http://a.test/", "cache": "c.json"},
	} {
		if _, err := parse(with); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestParse_Persona(t *testing.T) {
	r, err := parse(map[string]any{"persona": " A first-time visitor ", "goal": "Write a note", "url": "http://a.test/"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Persona != "A first-time visitor" || r.Goal != "Write a note" || r.Task != "" {
		t.Errorf("parsed %+v", r)
	}
}

func TestForLog_HidesSecrets(t *testing.T) {
	with := map[string]any{"task": "t", "secrets": map[string]any{"PASSWORD": "hunter2"}}
	b, _ := json.Marshal(forLog(with))
	if strings.Contains(string(b), "hunter2") || !strings.Contains(string(b), "PASSWORD") {
		t.Errorf("forLog = %s", b)
	}
	if with["secrets"].(map[string]any)["PASSWORD"] != "hunter2" {
		t.Errorf("forLog changed with")
	}
}

func outcome() scenario.Outcome {
	return scenario.Outcome{
		Verdict:  scenario.Pass,
		Reason:   "The dashboard says Welcome.",
		Evidence: []scenario.Evidence{{Quote: "Welcome, alice", Verified: true}},
		Stopped:  scenario.StoppedDone,
		Summary:  "Signed up.",
		Steps: []scenario.Step{
			{Index: 1, Tool: "navigate", Input: map[string]any{"url": "http://a.test/signup"}, Result: "Navigated.", URL: "http://a.test/signup", Screenshot: "/tmp/x/step-01.jpg"},
			{Index: 2, Tool: "left_click", Error: "Error: ref_9 is not on the page any more"},
		},
		URL:           "http://a.test/dashboard",
		Title:         "Dashboard",
		ConsoleErrors: []page.ConsoleError{{Type: "error", Text: "boom"}},
		NetworkErrors: []page.NetworkError{{Method: "POST", URL: "http://a.test/api", Status: 500}},
		Usage:         llm.Usage{Requests: 3, InputTokens: 1000, OutputTokens: 100},
		Model:         "claude-opus-5-5",
		Duration:      42 * time.Second,
	}
}

func TestResult_IsWhatProbeReads(t *testing.T) {
	r := req{Task: "t", Expect: "e", URL: "http://a.test/", Model: "claude-opus-5-5", Effort: "medium", MaxSteps: 40, Timeout: time.Minute}
	ret, err := result(r, outcome(), files{Report: "/tmp/x/report.md", Trace: "/tmp/x/trace.json"}, 42*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if ret["status"] != 0.0 || ret["rt"] != "42s" {
		t.Errorf("status %v, rt %v", ret["status"], ret["rt"])
	}
	res := ret["res"].(map[string]any)
	if res["verdict"] != "pass" || res["stopped"] != "done" || res["url"] != "http://a.test/dashboard" {
		t.Errorf("res %v", res)
	}
	if ev := res["evidence"].([]any)[0].(map[string]any); ev["quote"] != "Welcome, alice" || ev["verified"] != true {
		t.Errorf("evidence %v", ev)
	}
	steps := res["steps"].([]any)
	if len(steps) != 2 || steps[1].(map[string]any)["error"] == nil {
		t.Errorf("steps %v", steps)
	}
	if res["network_errors"].([]any)[0].(map[string]any)["status"] != 500.0 {
		t.Errorf("network_errors %v", res["network_errors"])
	}
	if fp := res["filepaths"].(map[string]any); fp["report"] != "/tmp/x/report.md" {
		t.Errorf("filepaths %v", fp)
	}
	report, _ := res["report"].(string)
	if !strings.Contains(report, "pass: The dashboard says Welcome.") || !strings.Contains(report, "/tmp/x/report.md") {
		t.Errorf("report %q", report)
	}
}

func TestEvidence_WritesTheReport(t *testing.T) {
	ev, err := newEvidence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := outcome()
	o.Steps[0].Screenshot = ev.save("step-01.jpg", []byte{0xff, 0xd8})
	f, err := ev.write(req{Task: "Sign up", Expect: "Welcome"}, o)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(f.Report)
	for _, want := range []string{"# ✅ PASS", "> Sign up", "✓ \"Welcome, alice\"", "### 2. left_click", "![step 1](step-01.jpg)", "POST http://a.test/api → 500", "boom"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("the report lacks %q:\n%s", want, md)
		}
	}
	var trace map[string]any
	data, _ := os.ReadFile(f.Trace)
	if err := json.Unmarshal(data, &trace); err != nil || trace["verdict"] != "pass" {
		t.Errorf("trace %s, %v", data, err)
	}
	if info, err := os.Stat(f.Report); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the report is readable by others: %v", info.Mode())
	}
}

func personaOutcome() scenario.Outcome {
	o := outcome()
	o.Mode = scenario.ModePersona
	o.Verdict, o.Reason = scenario.Fail, "No note could be written."
	o.Findings = []scenario.Finding{
		{Title: "The note page fails", Severity: "high", Category: "error", Expected: "A form", Observed: "A 500",
			Evidence: []scenario.Evidence{{Quote: "GET http://a.test/notes/new -> 500", Verified: true}}, Steps: []int{2}, Verified: true},
		{Title: "A typo", Severity: "low", Category: "content", Observed: "Welcom", Evidence: []scenario.Evidence{{Quote: "Welcom"}}},
	}
	o.Metrics = scenario.Metrics{Steps: 2, Requests: 9, SlowestRequests: []page.Request{{Method: "GET", URL: "http://a.test/api", Status: 200, DurationMS: 2400}}}
	return o
}

func TestResult_Persona(t *testing.T) {
	r := req{Persona: "p", Goal: "g", URL: "http://a.test/", Timeout: time.Minute}
	ret, err := result(r, personaOutcome(), files{Report: "/tmp/x/report.md"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	res := ret["res"].(map[string]any)
	findings := res["findings"].([]any)
	if res["mode"] != "persona" || len(findings) != 2 || findings[0].(map[string]any)["severity"] != "high" {
		t.Errorf("res %v", res)
	}
	if m := res["metrics"].(map[string]any); m["requests"] != 9.0 || m["slowest_requests"].([]any)[0].(map[string]any)["duration_ms"] != 2400.0 {
		t.Errorf("metrics %v", res["metrics"])
	}
	if q := ret["req"].(map[string]any); q["persona"] != "p" || q["goal"] != "g" || q["task"] != nil {
		t.Errorf("req %v", q)
	}
	report := res["report"].(string)
	if !strings.Contains(report, "2 findings") || !strings.Contains(report, "high: The note page fails") || !strings.Contains(report, "low: A typo (unverified)") {
		t.Errorf("report %q", report)
	}

	sc, _ := result(req{Task: "t", Expect: "e"}, outcome(), files{}, time.Second)
	if _, ok := sc["res"].(map[string]any)["findings"]; ok {
		t.Errorf("a scenario test has findings")
	}
}

func TestEvidence_WritesThePersonaReport(t *testing.T) {
	ev, err := newEvidence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f, err := ev.write(req{Persona: "A visitor", Goal: "Write a note"}, personaOutcome())
	if err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(f.Report)
	for _, want := range []string{"# ❌ FAIL", "## Persona\n\n> A visitor", "## Goal\n\n> Write a note", "### 1. 🔴 The note page fails", "- Steps: 2", "### 2. 🟡 A typo", "none of its evidence was found", "2400 ms: GET http://a.test/api"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("the report lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(string(md), "## Task") {
		t.Errorf("a persona report has a task")
	}
}

func TestParse_Cache(t *testing.T) {
	r, err := parse(map[string]any{"task": "t", "expect": "e", "url": "http://a.test/", "cache": " .probe-agent/signup.json "})
	if err != nil {
		t.Fatal(err)
	}
	if r.Cache != ".probe-agent/signup.json" || r.CacheMode != "auto" {
		t.Errorf("cache %q, mode %q", r.Cache, r.CacheMode)
	}
	if r, err := parse(map[string]any{"task": "t", "expect": "e", "url": "http://a.test/", "cache": "c.json", "cache_mode": "strict"}); err != nil || r.CacheMode != "strict" {
		t.Errorf("strict: %q, %v", r.CacheMode, err)
	}
}

func TestRecording_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "signup.json")
	if rec, err := loadRecording(path); rec != nil || err != nil {
		t.Fatalf("a missing recording: %v, %v", rec, err)
	}
	rec := &scenario.Recording{Version: 2, Task: "t", Expect: "e", Start: "/", Steps: []scenario.Recorded{
		{Tool: "left_click", Locator: &page.Locator{Role: "link", Name: "Sign up"}, Path: "/signup"},
	}}
	if err := saveRecording(path, rec); err != nil {
		t.Fatal(err)
	}
	got, err := loadRecording(path)
	if err != nil || got.Steps[0].Locator.Name != "Sign up" || got.Steps[0].Path != "/signup" {
		t.Errorf("loaded %+v, %v", got, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(data), "}\n") || !strings.Contains(string(data), "\n  \"steps\"") {
		t.Errorf("the recording is not written for a repository to keep:\n%s", data)
	}
	os.WriteFile(path, []byte("not json"), 0o644)
	if _, err := loadRecording(path); err == nil {
		t.Errorf("loaded a broken recording")
	}
}

func TestResult_Cache(t *testing.T) {
	r := req{Task: "t", Expect: "e", Cache: "c.json", CacheMode: "auto"}
	o := outcome()
	o.Replay = &scenario.Replay{Status: scenario.ReplayPartial, Replayed: 3, DivergedAt: 4, Reason: "the page has no button \"Next\"", Stale: true}
	ret, err := result(r, o, files{Report: "/tmp/r.md", Recording: "c.json"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	res := ret["res"].(map[string]any)
	c := res["cache"].(map[string]any)
	if c["status"] != "partial" || c["replayed"] != 3.0 || c["diverged_at"] != 4.0 || c["stale"] != true || c["saved"] != true || c["path"] != "c.json" {
		t.Errorf("cache %v", c)
	}
	if report := res["report"].(string); !strings.Contains(report, "cache: partial, 3 steps replayed, then the tester from step 4") || !strings.Contains(report, "recorded to c.json") {
		t.Errorf("report %q", report)
	}

	miss := cacheOf(req{Cache: "c.json", CacheMode: "read"}, outcome(), false)
	if miss.Status != "miss" || !strings.Contains(miss.Reason, "no recording at c.json") {
		t.Errorf("miss %+v", miss)
	}
	if cacheOf(req{}, outcome(), false) != nil {
		t.Errorf("a test without a cache has res.cache")
	}
	none, _ := result(req{Task: "t"}, outcome(), files{}, time.Second)
	if _, ok := none["res"].(map[string]any)["cache"]; ok {
		t.Errorf("res.cache without a cache")
	}
}
