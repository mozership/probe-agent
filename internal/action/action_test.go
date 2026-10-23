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
		"no task":        {"expect": "e", "url": "http://a.test/"},
		"no expect":      {"task": "t", "url": "http://a.test/"},
		"no url":         {"task": "t", "expect": "e"},
		"a url not http": {"task": "t", "expect": "e", "url": "file:///etc/passwd"},
		"a bad timeout":  {"task": "t", "expect": "e", "url": "http://a.test/", "timeout": "soon"},
		"zero steps":     {"task": "t", "expect": "e", "url": "http://a.test/", "max_steps": 0.0},
		"a bad effort":   {"task": "t", "expect": "e", "url": "http://a.test/", "effort": "huge"},
		"a bad secret":   {"task": "t", "expect": "e", "url": "http://a.test/", "secrets": map[string]any{"MY-PW": "x"}},
		"an unknown key": {"task": "t", "expect": "e", "url": "http://a.test/", "prompt": "x"},
	} {
		if _, err := parse(with); err == nil {
			t.Errorf("%s: parsed", name)
		}
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
