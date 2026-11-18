package action

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mozership/probe-agent/internal/scenario"
)

func TestParse_Targets(t *testing.T) {
	r, err := parse(map[string]any{
		"task": "t", "expect": "e",
		"targets": map[string]any{
			"browser": map[string]any{"url": "https://app.test/", "allow_hosts": []any{"app.test", "auth.test"}, "headless": "false", "window_w": 1024.0},
			"ios":     map[string]any{"app": "com.example.app", "device": "iPhone 17"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Browser || r.ShortForm || r.URL != "https://app.test/" || r.Headless || r.Width != 1024 {
		t.Errorf("browser %+v", r)
	}
	if !slices.Equal(r.AllowHosts, []string{"app.test", "auth.test"}) {
		t.Errorf("allow_hosts %v", r.AllowHosts)
	}
	if r.IOS == nil || r.IOS.App != "com.example.app" || r.IOS.Device != "iPhone 17" {
		t.Errorf("ios %+v", r.IOS)
	}
	if got := r.kinds(); !slices.Equal(got, []string{"browser", "ios"}) {
		t.Errorf("kinds %v", got)
	}
}

func TestParse_TargetsIOSAlone(t *testing.T) {
	r, err := parse(map[string]any{
		"task": "t", "expect": "e",
		"targets": map[string]any{"ios": map[string]any{"app": "com.apple.Preferences"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Browser || r.URL != "" || r.IOS == nil || r.IOS.App != "com.apple.Preferences" {
		t.Errorf("parsed %+v", r)
	}
}

// The parameters of the browser at the top of with are the short form of
// targets.browser, as before there were targets.
func TestParse_ShortFormOfTheBrowser(t *testing.T) {
	r, err := parse(map[string]any{"task": "t", "expect": "e", "url": "http://a.test/"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Browser || !r.ShortForm || r.IOS != nil || r.URL != "http://a.test/" {
		t.Errorf("parsed %+v", r)
	}
}

func TestParse_RefusesTargets(t *testing.T) {
	ios := map[string]any{"app": "com.example.app"}
	for name, tt := range map[string]struct {
		with map[string]any
		want string
	}{
		"both forms": {
			with: map[string]any{"task": "t", "expect": "e", "url": "http://a.test/", "targets": map[string]any{"ios": ios}},
			want: "url goes under targets.browser",
		},
		"not a map":      {with: map[string]any{"task": "t", "expect": "e", "targets": "ios"}, want: "targets must be a map"},
		"no target":      {with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{}}, want: "names no target"},
		"a later target": {with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"http": map[string]any{"url": "http://a.test/"}}}, want: "targets.http is not driven yet"},
		"an unknown target": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"android": map[string]any{}}},
			want: "unknown target android",
		},
		"an unknown key of a target": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"ios": map[string]any{"app": "a", "bundle": "b"}}},
			want: "unknown parameter targets.ios.bundle",
		},
		"no app": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"ios": map[string]any{"device": "iPhone 17"}}},
			want: "targets.ios.app is required",
		},
		"an ios with nothing under it": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"ios": nil}},
			want: "targets.ios.app is required",
		},
		"no url of the browser": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"browser": map[string]any{"headless": true}}},
			want: "targets.browser.url is required",
		},
		"a bad url of the browser": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"browser": map[string]any{"url": "file:///etc/passwd"}}},
			want: "targets.browser.url",
		},
		"a bad timeout of the app": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"ios": map[string]any{"app": "a", "timeout": "soon"}}},
			want: "targets.ios.timeout",
		},
		"a cache with an app": {
			with: map[string]any{"task": "t", "expect": "e", "cache": "c.json", "targets": map[string]any{"ios": ios}},
			want: "cache is for a test of a browser alone",
		},
	} {
		_, err := parse(tt.with)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err %v, want it to say %q", name, err, tt.want)
		}
	}
}

func TestActionNamed(t *testing.T) {
	sha := strings.Repeat("a", 40)
	tests := []struct {
		name    string
		repo    string
		actions map[string]string
		want    string
		err     string
	}{
		{name: "by any name", repo: probeIOS, actions: map[string]string{"iphone": "github.com/mozership/probe-ios@" + sha, "agent": "github.com/mozership/probe-agent@" + sha}, want: "iphone"},
		{name: "the browser", repo: probeBrowser, actions: map[string]string{"web": "github.com/mozership/probe-browser@" + sha, "ios": "github.com/mozership/probe-ios@" + sha}, want: "web"},
		{name: "none", repo: probeBrowser, actions: map[string]string{"agent": "github.com/mozership/probe-agent@" + sha}, err: "through probe-browser: name it under actions of the workflow, as browser: github.com/mozership/probe-browser@<commit SHA>"},
		{name: "a local action is not it", repo: probeIOS, actions: map[string]string{"ios": "/src/probe-ios"}, err: "name it under actions"},
		{name: "two", repo: probeIOS, actions: map[string]string{"a": "github.com/mozership/probe-ios@" + sha, "b": "github.com/mozership/probe-ios@" + strings.Repeat("b", 40)}, err: "but a and b under actions each name github.com/mozership/probe-ios"},
	}
	for _, tt := range tests {
		got, err := actionNamed(tt.actions, tt.repo, "it drives its target", "browser")
		switch {
		case tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)):
			t.Errorf("%s: err %v, want %q", tt.name, err, tt.err)
		case tt.err == "" && (err != nil || got != tt.want):
			t.Errorf("%s: %q, %v, want %q", tt.name, got, err, tt.want)
		}
	}
}

func TestResult_Targets(t *testing.T) {
	r := req{Task: "t", Expect: "e", IOS: &iosReq{App: "com.example.app", Device: "iPhone 17"}, Model: "claude-opus-5-5", Effort: "medium", MaxSteps: 40}
	o := outcome()
	o.URL, o.Title = "", ""
	o.Targets = []scenario.TargetState{{Kind: "ios", App: "com.example.app", Device: "UDID-1"}}
	ret, err := result(r, o, files{Report: "/tmp/x/report.md"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	req := ret["req"].(map[string]any)
	if _, ok := req["url"]; ok || req["app"] != "com.example.app" || req["device"] != "iPhone 17" {
		t.Errorf("req %v", req)
	}
	if kinds := req["targets"].([]any); len(kinds) != 1 || kinds[0] != "ios" {
		t.Errorf("req.targets %v", req["targets"])
	}
	targets := ret["res"].(map[string]any)["targets"].([]any)
	if len(targets) != 1 || targets[0].(map[string]any)["device"] != "UDID-1" {
		t.Errorf("res.targets %v", targets)
	}
}

func TestEvidence_WritesTheFinalScreenOfTheApp(t *testing.T) {
	ev, err := newEvidence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := outcome()
	o.Targets = []scenario.TargetState{
		{Kind: "browser", URL: "http://a.test/dashboard", Title: "Dashboard", Screenshot: ev.save("final.jpg", []byte{0xff, 0xd8})},
		{Kind: "ios", App: "com.example.app", Device: "UDID-1", Screenshot: ev.save("final-ios.png", []byte{0x89, 'P'})},
	}
	f, err := ev.write(req{Task: "t", Expect: "e"}, o)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(f.Report)
	for _, want := range []string{"## Final page\n\nDashboard (<http://a.test/dashboard>)", "![final page](final.jpg)", "## Final screen of the app\n\ncom.example.app on UDID-1", "![final screen](final-ios.png)"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("the report lacks %q:\n%s", want, md)
		}
	}
}

func TestParse_TimeoutOfTheApp(t *testing.T) {
	for _, v := range []any{"5s", 5.0, "5"} {
		r, err := parse(map[string]any{
			"task": "t", "expect": "e",
			"targets": map[string]any{"ios": map[string]any{"app": "a", "timeout": v}},
		})
		if err != nil {
			t.Fatalf("%v: %v", v, err)
		}
		if r.IOS.Timeout != 5*time.Second {
			t.Errorf("%v: timeout %s, want 5s", v, r.IOS.Timeout)
		}
	}
}

func TestIOSWith(t *testing.T) {
	actions := []map[string]any{{"name": "tap", "selector": "go"}, {"name": "tree"}}
	got := iosWith(iosReq{App: "com.example.app"}, actions)
	want := map[string]any{"app": "com.example.app", "actions": []any{actions[0], actions[1]}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with %v, want %v without device and timeout", got, want)
	}
	got = iosWith(iosReq{App: "com.example.app", Device: "iPhone 17", Timeout: 5 * time.Second}, actions)
	if got["device"] != "iPhone 17" || got["timeout"] != "5s" {
		t.Errorf("with %v, want the device and the timeout", got)
	}
}

func TestSecretsHidden(t *testing.T) {
	var b strings.Builder
	w := secretsHidden(&b, map[string]string{"EMAIL": "dave@example.test", "OTP": "123456"})
	if _, err := w.Write([]byte(`{"actions":[{"value":"dave@example.test"},{"value":"say \"dave@example.test\""},{"value":"123456"}]}` + "\n")); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if strings.Contains(got, "dave@example.test") || strings.Contains(got, "123456") {
		t.Errorf("a secret is in %s", got)
	}
	if !strings.Contains(got, "<secret:EMAIL>") {
		t.Errorf("the secret is not named in %s", got)
	}

	// A short one, one in another case, and one JSON quotes.
	b.Reset()
	w = secretsHidden(&b, map[string]string{"PIN": "739", "EMAIL": "Dave+X@example.test", "PW": `p"w\x`})
	if _, err := w.Write([]byte(`{"value":"pin 739 for dave+x@example.test, pw p\"w\\x"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != `{"value":"pin <secret:PIN> for <secret:EMAIL>, pw <secret:PW>"}`+"\n" {
		t.Errorf("got %s", got)
	}
}
