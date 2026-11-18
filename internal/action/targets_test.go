package action

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mozership/probe"
	"github.com/mozership/probe/actionrpc"

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
		"a later target": {with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"grpc": map[string]any{"addr": "a.test:443"}}}, want: "targets.grpc is not driven yet"},
		"no url of the API": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"http": map[string]any{"headers": map[string]any{"X": "y"}}}},
			want: "targets.http.url is required",
		},
		"a url of the API not a string": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"http": map[string]any{"url": 1.0}}},
			want: "targets.http.url must be a string",
		},
		"a bad url of the API": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"http": map[string]any{"url": "ftp://a.test/"}}},
			want: "targets.http.url \"ftp://a.test/\" must be an http or https URL",
		},
		"an unknown key of the API": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"http": map[string]any{"url": "http://a.test/", "method": "GET"}}},
			want: "unknown parameter targets.http.method",
		},
		"headers of the API not a map": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"http": map[string]any{"url": "http://a.test/", "headers": "X: y"}}},
			want: "targets.http.headers must be a map",
		},
		"a header of the API not a string": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"http": map[string]any{"url": "http://a.test/", "headers": map[string]any{"X": []any{"y"}}}}},
			want: "targets.http.headers.X must be a string",
		},
		"an openapi of the API without spec": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"http": map[string]any{"url": "http://a.test/", "openapi": map[string]any{"strict": true}}}},
			want: "targets.http.openapi.spec is required",
		},
		"an unknown key of openapi": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"http": map[string]any{"url": "http://a.test/", "openapi": map[string]any{"spec": "o.yml", "request": false}}}},
			want: "unknown parameter targets.http.openapi.request",
		},
		"a bad timeout of the API": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"http": map[string]any{"url": "http://a.test/", "timeout": "soon"}}},
			want: "targets.http.timeout",
		},
		"a cache with an API": {
			with: map[string]any{"task": "t", "expect": "e", "cache": "c.json", "targets": map[string]any{"browser": map[string]any{"url": "http://a.test/"}, "http": map[string]any{"url": "http://a.test/api"}}},
			want: "cache is for a test of a browser alone",
		},
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

func TestParse_TargetsHTTP(t *testing.T) {
	r, err := parse(map[string]any{
		"persona": "p", "goal": "g",
		"targets": map[string]any{
			"http": map[string]any{
				"url":     "https://api.test/v1",
				"openapi": "./openapi.yml",
				"headers": map[string]any{"Authorization": "Bearer t", "X-Version": 2.0},
				"cookies": map[string]any{"session": "s"},
				"timeout": "10s",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Browser || r.IOS != nil || r.HTTP == nil {
		t.Fatalf("parsed %+v", r)
	}
	h := r.HTTP
	if h.URL != "https://api.test/v1" || h.spec() != "./openapi.yml" || h.Timeout != 10*time.Second {
		t.Errorf("http %+v", h)
	}
	if !reflect.DeepEqual(h.Headers, map[string]string{"Authorization": "Bearer t", "X-Version": "2"}) || h.Cookies["session"] != "s" {
		t.Errorf("headers %v, cookies %v", h.Headers, h.Cookies)
	}
	if got := r.kinds(); !slices.Equal(got, []string{"http"}) {
		t.Errorf("kinds %v", got)
	}

	r, err = parse(map[string]any{
		"task": "t", "expect": "e",
		"targets": map[string]any{"http": map[string]any{"url": "http://a.test/", "openapi": map[string]any{"spec": "o.yml", "strict": "true"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.HTTP.OpenAPI, map[string]any{"spec": "o.yml", "strict": true}) {
		t.Errorf("openapi %v", r.HTTP.OpenAPI)
	}
}

func TestHTTPWith(t *testing.T) {
	h := httpReq{
		URL:     "https://api.test/v1",
		OpenAPI: map[string]any{"spec": "o.yml"},
		Headers: map[string]string{"Authorization": "Bearer t"},
		Cookies: map[string]string{"session": "s"},
		Timeout: 10 * time.Second,
	}
	got := httpWith(h, scenario.HTTPRequest{
		Method:  "POST",
		URL:     "https://api.test/v1/orders",
		Headers: map[string]string{"Content-Type": "application/json", "Authorization": "mine"},
		Body:    `{"item":1}`,
	})
	want := map[string]any{
		"method":       "POST",
		"url":          "https://api.test/v1/orders",
		"keep_cookies": true,
		"headers":      map[string]any{"Content-Type": "application/json", "Authorization": "Bearer t"},
		"body":         `{"item":1}`,
		"cookies":      map[string]any{"session": "s"},
		"openapi":      map[string]any{"spec": "o.yml"},
		"timeout":      "10s",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with %v, want %v", got, want)
	}

	got = httpWith(httpReq{URL: "http://a.test/"}, scenario.HTTPRequest{Method: "GET", URL: "http://a.test/x"})
	if !reflect.DeepEqual(got, map[string]any{"method": "GET", "url": "http://a.test/x", "keep_cookies": true}) {
		t.Errorf("with %v, want only the method, the url and keep_cookies", got)
	}
}

func TestHTTPResponse(t *testing.T) {
	cr := &probe.CallResult{
		Result: map[string]any{"res": map[string]any{
			"code": 201.0, "status": "201 Created",
			"headers": map[string]any{"Content-Type": "application/json"},
			"body":    `{"id":7}`,
		}},
		Contract: &actionrpc.ContractCheck{
			Matched:    &actionrpc.ContractMatch{Spec: "o.yml", Operation: "POST /orders", Response: "201"},
			Violations: []actionrpc.Violation{{In: "response", Field: "$.id", Reason: "type", Message: "id must be a string"}},
		},
	}
	got, err := httpResponse(cr)
	if err != nil {
		t.Fatal(err)
	}
	want := scenario.HTTPResponse{
		Code: 201, Status: "201 Created",
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       `{"id":7}`,
		Operation:  "POST /orders",
		Violations: []scenario.Violation{{In: "response", Field: "$.id", Reason: "type", Message: "id must be a string"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("response %+v, want %+v", got, want)
	}

	// A body that came parsed is given as JSON, and one that is not text
	// by its file.
	got, _ = httpResponse(&probe.CallResult{Result: map[string]any{"res": map[string]any{"code": 200, "body": map[string]any{"a": 1.0}}}})
	if got.Body != `{"a":1}` {
		t.Errorf("body %q", got.Body)
	}
	got, _ = httpResponse(&probe.CallResult{Result: map[string]any{"res": map[string]any{"code": 200, "body": "", "filepath": "/tmp/x.png"}}})
	if got.File != "/tmp/x.png" || got.Body != "" {
		t.Errorf("response %+v", got)
	}
	if _, err := httpResponse(&probe.CallResult{Result: map[string]any{}}); err == nil {
		t.Error("a result without res is taken")
	}
}

func TestAPIGuard(t *testing.T) {
	g := apiGuard(actionrpc.Guard{}, "https://api.test/v1")
	if !slices.Equal(g.AllowHosts, []string{"api.test"}) || !g.Runs("http") || g.AllowsHost("evil.test") || !g.AllowsHost("api.test:443") {
		t.Errorf("guard %+v", g)
	}
	g = apiGuard(actionrpc.Guard{ReadOnly: true, AllowHosts: []string{"*.test"}}, "http://api.test:8080/")
	if !g.ReadOnly || !slices.Equal(g.AllowHosts, []string{"api.test:8080"}) || !g.Runs("http") {
		t.Errorf("guard %+v", g)
	}
	// A host the run does not allow stays refused.
	g = apiGuard(actionrpc.Guard{AllowHosts: []string{"other.test"}}, "https://api.test/")
	if !slices.Equal(g.AllowHosts, []string{"other.test"}) {
		t.Errorf("guard %+v", g)
	}
}

func TestHTTPTarget(t *testing.T) {
	caller := probe.NewActionCaller(actionrpc.Call{}, "")
	t.Setenv(probe.ExecutableEnv, "")
	if _, err := httpTarget(caller, nil, httpReq{URL: "http://a.test/"}); err == nil || !strings.Contains(err.Error(), "needs Probe v1.37.0 or later") {
		t.Errorf("without %s: err %v", probe.ExecutableEnv, err)
	}

	t.Setenv(probe.ExecutableEnv, "/usr/local/bin/probe")
	if _, err := httpTarget(caller, map[string]string{"http": "github.com/x/y@" + strings.Repeat("a", 40)}, httpReq{URL: "http://a.test/"}); err == nil || !strings.Contains(err.Error(), "give the name http to github.com/x/y") {
		t.Errorf("an action named http: err %v", err)
	}
	if _, err := httpTarget(caller, nil, httpReq{URL: "http://a.test/", OpenAPI: map[string]any{"spec": "missing.yml"}}); err == nil || !strings.Contains(err.Error(), "targets.http.openapi") {
		t.Errorf("a missing document: err %v", err)
	}

	spec := t.TempDir() + "/openapi.yml"
	if err := os.WriteFile(spec, []byte("openapi: 3.0.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	api, err := httpTarget(caller, nil, httpReq{
		URL:     "http://a.test/",
		OpenAPI: map[string]any{"spec": spec},
		Headers: map[string]string{"X-Version": "2", "Authorization": "Bearer t"},
		Cookies: map[string]string{"session": "s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if api.OpenAPI != "openapi: 3.0.3\n" || !slices.Equal(api.Fixed, []string{"Authorization", "X-Version", "Cookie"}) {
		t.Errorf("api %+v", api)
	}
}

func TestEvidence_WritesTheLastResponseOfTheAPI(t *testing.T) {
	ev, err := newEvidence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := outcome()
	o.Targets = []scenario.TargetState{{Kind: "http", Method: "GET", URL: "http://a.test/v1/orders/7", Status: 200}}
	f, err := ev.write(req{Task: "t", Expect: "e"}, o)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(f.Report)
	if want := "## Last response of the API\n\nGET <http://a.test/v1/orders/7> → 200"; !strings.Contains(string(md), want) {
		t.Errorf("the report lacks %q:\n%s", want, md)
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
