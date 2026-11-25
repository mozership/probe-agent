package action

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mozership/probe/sdk"

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
	if !r.Browser || r.URL != "https://app.test/" || r.Headless || r.Width != 1024 {
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

func TestParse_TargetsAndroid(t *testing.T) {
	r, err := parse(map[string]any{
		"task": "t", "expect": "e",
		"targets": map[string]any{
			"android": map[string]any{"app": "com.android.settings", "device": "emulator-5554", "timeout": "10s"},
			"ios":     map[string]any{"app": "com.apple.Preferences"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Android == nil || r.Android.App != "com.android.settings" || r.Android.Device != "emulator-5554" || r.Android.Timeout != 10*time.Second {
		t.Errorf("android %+v", r.Android)
	}
	if got := r.kinds(); !slices.Equal(got, []string{"ios", "android"}) {
		t.Errorf("kinds %v", got)
	}
	if _, err := appTarget(nil, map[string]string{"agent": "./action"}, probeAndroid, targetAndroid, *r.Android); err == nil || !strings.Contains(err.Error(), "targets.android drives the app through probe-android: name it under actions of the workflow, as android: github.com/mozership/probe-android@<commit SHA>") {
		t.Errorf("without probe-android: err %v", err)
	}
}

func TestResult_AppOfAndroid(t *testing.T) {
	r := req{Task: "t", Expect: "e", Android: &appReq{App: "com.android.settings", Device: "emulator-5554"}}
	ret, err := result(r, outcome(), files{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if q := ret["req"].(map[string]any); q["app"] != "com.android.settings" || q["device"] != "emulator-5554" {
		t.Errorf("req %v", q)
	}
	// With an iOS app too, the app and the device are of it.
	r.IOS = &appReq{App: "com.example.app"}
	ret, _ = result(r, outcome(), files{}, 0)
	if q := ret["req"].(map[string]any); q["app"] != "com.example.app" || q["device"] != nil {
		t.Errorf("req %v", q)
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

// The parameters of the browser at the top of with, a short form of
// targets.browser once, are refused, each with where it goes; and a step
// without targets names nothing to drive.
func TestParse_RefusesTheShortFormOfTheBrowser(t *testing.T) {
	for _, k := range []string{"url", "allow_hosts", "storage_state", "headless", "window_w", "window_h"} {
		_, err := parse(map[string]any{"task": "t", "expect": "e", k: "x", "targets": browserAt("http://a.test/")})
		if err == nil || !strings.Contains(err.Error(), k+" goes under targets.browser, as targets.browser."+k) || strings.Contains(err.Error(), "unknown parameter "+k) {
			t.Errorf("%s: err %v", k, err)
		}
	}
	if _, err := parse(map[string]any{"task": "t", "expect": "e"}); err == nil || !strings.Contains(err.Error(), "targets is required") {
		t.Errorf("without targets: err %v", err)
	}
	if slices.Contains(Params(), "url") {
		t.Error("url is still a parameter")
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
		"not a map": {with: map[string]any{"task": "t", "expect": "e", "targets": "ios"}, want: "targets must be a map"},
		"no target": {with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{}}, want: "names no target"},
		"no app of android": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"android": map[string]any{"device": "emulator-5554"}}},
			want: "targets.android.app is required",
		},
		"a bad timeout of android": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"android": map[string]any{"app": "a", "timeout": "soon"}}},
			want: "targets.android.timeout",
		},
		"an unknown key of android": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"android": map[string]any{"app": "a", "args": []any{}}}},
			want: "unknown parameter targets.android.args",
		},
		"no addr of the gRPC API": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"grpc": map[string]any{"tls": true}}},
			want: "targets.grpc.addr is required",
		},
		"a bad protocol of the gRPC API": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"grpc": map[string]any{"addr": "a.test:443", "protocol": "grpc-web"}}},
			want: "targets.grpc.protocol must be grpc or connect",
		},
		"a proto not a map": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"grpc": map[string]any{"addr": "a.test:443", "proto": "users.proto"}}},
			want: "targets.grpc.proto must be a map of files",
		},
		"a proto without files": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"grpc": map[string]any{"addr": "a.test:443", "proto": map[string]any{"strict": true}}}},
			want: "targets.grpc.proto.files is required",
		},
		"files of a proto not a list": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"grpc": map[string]any{"addr": "a.test:443", "proto": map[string]any{"files": "users.proto"}}}},
			want: "targets.grpc.proto.files must be a list of paths",
		},
		"no files of a proto": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"grpc": map[string]any{"addr": "a.test:443", "proto": map[string]any{"files": []any{}}}}},
			want: "targets.grpc.proto.files names no file",
		},
		"import_paths of a proto not paths": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"grpc": map[string]any{"addr": "a.test:443", "proto": map[string]any{"files": []any{"a.proto"}, "import_paths": []any{1.0}}}}},
			want: "targets.grpc.proto.import_paths must be a list of paths",
		},
		"an unknown key of a proto": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"grpc": map[string]any{"addr": "a.test:443", "proto": map[string]any{"files": []any{"a.proto"}, "paths": []any{"."}}}}},
			want: "unknown parameter targets.grpc.proto.paths",
		},
		"metadata of the gRPC API not a map": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"grpc": map[string]any{"addr": "a.test:443", "metadata": "x"}}},
			want: "targets.grpc.metadata must be a map",
		},
		"an unknown key of the gRPC API": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"grpc": map[string]any{"addr": "a.test:443", "service": "S"}}},
			want: "unknown parameter targets.grpc.service",
		},
		"no url of the GraphQL API": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"graphql": map[string]any{"timeout": "5s"}}},
			want: "targets.graphql.url is required",
		},
		"a bad url of the GraphQL API": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"graphql": map[string]any{"url": "ws://a.test/graphql"}}},
			want: "targets.graphql.url \"ws://a.test/graphql\" must be an http or https URL",
		},
		"headers of the GraphQL API not a map": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"graphql": map[string]any{"url": "http://a.test/graphql", "headers": "X: y"}}},
			want: "targets.graphql.headers must be a map",
		},
		"an unknown key of the GraphQL API": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"graphql": map[string]any{"url": "http://a.test/graphql", "query": "{ a }"}}},
			want: "unknown parameter targets.graphql.query; the parameters of graphql are url, headers, timeout",
		},
		"no url of the mailbox": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"mail": map[string]any{"timeout": "5s"}}},
			want: "targets.mail.url is required",
		},
		"a bad url of the mailbox": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"mail": map[string]any{"url": "imap://mail.test"}}},
			want: "targets.mail.url \"imap://mail.test\" must be the http or https URL of a JMAP server",
		},
		"basic_auth of the mailbox not a map": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"mail": map[string]any{"url": "http://mail.test", "basic_auth": "u:p"}}},
			want: "targets.mail.basic_auth must be a map of username and password",
		},
		"basic_auth of the mailbox without a password": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"mail": map[string]any{"url": "http://mail.test", "basic_auth": map[string]any{"username": "u"}}}},
			want: "targets.mail.basic_auth must be a map of username and password",
		},
		"an empty account of the mailbox": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"mail": map[string]any{"url": "http://mail.test", "account_id": " "}}},
			want: "targets.mail.account_id must be the id of an account",
		},
		"an unknown key of the mailbox": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"mail": map[string]any{"url": "http://mail.test", "imap": true}}},
			want: "unknown parameter targets.mail.imap; the parameters of mail are url, basic_auth, headers, account_id, timeout",
		},
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
		"an unknown target": {
			with: map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"windows": map[string]any{}}},
			want: "unknown target windows",
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

func TestParse_TargetsGRPC(t *testing.T) {
	r, err := parse(map[string]any{
		"task": "t", "expect": "e",
		"targets": map[string]any{
			"grpc": map[string]any{
				"addr": "api.test:443", "tls": "true", "ca_file": "ca.pem", "timeout": 10.0,
				"metadata": map[string]any{"authorization": "Bearer t"},
				"proto":    map[string]any{"files": []any{"users.proto"}, "import_paths": []any{"."}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	g := r.GRPC
	if g == nil || g.Addr != "api.test:443" || !reflect.DeepEqual(g.Conn, map[string]any{"tls": true, "ca_file": "ca.pem", "timeout": "10s"}) {
		t.Fatalf("grpc %+v", g)
	}
	if g.Metadata["authorization"] != "Bearer t" || g.Proto == nil {
		t.Errorf("grpc %+v", g)
	}
	if got := r.kinds(); !slices.Equal(got, []string{"grpc"}) {
		t.Errorf("kinds %v", got)
	}
}

func TestGRPCWith(t *testing.T) {
	g := grpcReq{
		Addr:     "api.test:443",
		Conn:     map[string]any{"tls": true},
		Metadata: map[string]string{"authorization": "Bearer t"},
		Proto:    map[string]any{"files": []any{"users.proto"}},
	}
	got := grpcWith(g, &scenario.GRPCRequest{Service: "users.v1.UserService", Method: "GetUser", Body: `{"id":"1"}`, Metadata: map[string]string{"x-trace": "1", "authorization": "mine"}})
	want := map[string]any{
		"addr": "api.test:443", "tls": true, "service": "users.v1.UserService", "method": "GetUser", "body": `{"id":"1"}`,
		"metadata": map[string]any{"x-trace": "1", "authorization": "Bearer t"},
		"proto":    map[string]any{"files": []any{"users.proto"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with %v, want %v", got, want)
	}
	if _, ok := g.Conn["service"]; ok {
		t.Error("the connection of the target was changed")
	}
	got = grpcWith(grpcReq{Addr: "a.test:50051"}, nil)
	if !reflect.DeepEqual(got, map[string]any{"addr": "a.test:50051"}) {
		t.Errorf("with of a listing %v", got)
	}
}

func TestGRPCResponse(t *testing.T) {
	got, err := grpcResponse(&sdk.CallResult{
		Result: map[string]any{"res": map[string]any{
			"status_code": "OK", "status_message": "", "metadata": map[string]any{"x-id": "7"},
			"body": `{"user":{"id":"1"}}`, "messages": []any{map[string]any{"id": "1"}, map[string]any{"id": "2"}},
		}},
		Contract: &sdk.ContractCheck{
			Matched:    &sdk.ContractMatch{Operation: "users.v1.UserService/GetUser"},
			Violations: []sdk.Violation{{In: "response", Field: "$.user.id", Message: "bad"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := scenario.GRPCResponse{
		Code: "OK", Metadata: map[string]string{"x-id": "7"}, Body: `{"user":{"id":"1"}}`,
		Messages: []string{`{"id":"1"}`, `{"id":"2"}`}, Operation: "users.v1.UserService/GetUser",
		Violations: []scenario.Violation{{In: "response", Field: "$.user.id", Message: "bad"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("response %+v, want %+v", got, want)
	}
}

func TestGRPCTarget(t *testing.T) {
	caller := sdk.NewCaller(sdk.Call{}, "")
	t.Setenv(sdk.ExecutableEnv, "")
	if _, err := grpcTarget(caller, nil, grpcReq{Addr: "a.test:443"}); err == nil || !strings.Contains(err.Error(), "needs Probe v1.38.0 or later") {
		t.Errorf("without %s: err %v", sdk.ExecutableEnv, err)
	}
	t.Setenv(sdk.ExecutableEnv, "/usr/local/bin/probe")
	if _, err := grpcTarget(caller, map[string]string{"grpc": "./mine"}, grpcReq{Addr: "a.test:443"}); err == nil || !strings.Contains(err.Error(), "give the name grpc to ./mine") {
		t.Errorf("an action named grpc: err %v", err)
	}
	rpc, err := grpcTarget(caller, nil, grpcReq{Addr: "a.test:443", Metadata: map[string]string{"b": "1", "a": "2"}, Proto: map[string]any{"files": []any{"x.proto"}}})
	if err != nil || !slices.Equal(rpc.Fixed, []string{"a", "b"}) || !rpc.Proto {
		t.Errorf("rpc %+v, %v", rpc, err)
	}
}

func TestEvidence_WritesTheLastAnswerOfTheGRPCAPI(t *testing.T) {
	ev, err := newEvidence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := outcome()
	o.Targets = []scenario.TargetState{{Kind: "grpc", Address: "a.test:443", Method: "users.v1.UserService/GetUser", Code: "NOT_FOUND", Response: "users.v1.UserService/GetUser -> NOT_FOUND"}}
	f, err := ev.write(req{Task: "t", Expect: "e"}, o)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(f.Report)
	if want := "## Last answer of the gRPC API\n\nusers.v1.UserService/GetUser at a.test:443 → NOT_FOUND\n\n```\nusers.v1.UserService/GetUser -> NOT_FOUND\n```"; !strings.Contains(string(md), want) {
		t.Errorf("the report lacks %q:\n%s", want, md)
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
	cr := &sdk.CallResult{
		Result: map[string]any{"res": map[string]any{
			"code": 201.0, "status": "201 Created",
			"headers": map[string]any{"Content-Type": "application/json"},
			"body":    `{"id":7}`,
		}},
		Contract: &sdk.ContractCheck{
			Matched:    &sdk.ContractMatch{Spec: "o.yml", Operation: "POST /orders", Response: "201"},
			Violations: []sdk.Violation{{In: "response", Field: "$.id", Reason: "type", Message: "id must be a string"}},
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
	got, _ = httpResponse(&sdk.CallResult{Result: map[string]any{"res": map[string]any{"code": 200, "body": map[string]any{"a": 1.0}}}})
	if got.Body != `{"a":1}` {
		t.Errorf("body %q", got.Body)
	}
	got, _ = httpResponse(&sdk.CallResult{Result: map[string]any{"res": map[string]any{"code": 200, "body": "", "filepath": "/tmp/x.png"}}})
	if got.File != "/tmp/x.png" || got.Body != "" {
		t.Errorf("response %+v", got)
	}
	if _, err := httpResponse(&sdk.CallResult{Result: map[string]any{}}); err == nil {
		t.Error("a result without res is taken")
	}
}

func TestAPIGuard(t *testing.T) {
	g := apiGuard(sdk.Guard{}, "https://api.test/v1")
	// The port of the scheme is named, so that neither another port nor
	// plain http is allowed.
	if !slices.Equal(g.AllowHosts, []string{"api.test:443"}) || !g.Runs("http") || g.AllowsHost("evil.test:443") || g.AllowsHost("api.test:80") || !g.AllowsHost("api.test:443") {
		t.Errorf("guard %+v", g)
	}
	g = apiGuard(sdk.Guard{ReadOnly: true, AllowHosts: []string{"*.test"}}, "http://api.test:8080/")
	if !g.ReadOnly || !slices.Equal(g.AllowHosts, []string{"api.test:8080"}) || !g.Runs("http") {
		t.Errorf("guard %+v", g)
	}
	// A host the run does not allow stays refused.
	g = apiGuard(sdk.Guard{AllowHosts: []string{"other.test"}}, "https://api.test/")
	if !slices.Equal(g.AllowHosts, []string{"other.test"}) {
		t.Errorf("guard %+v", g)
	}
}

func TestResult_MasksTheBaseURLOfTheAPI(t *testing.T) {
	r := req{Task: "t", Expect: "e", HTTP: &httpReq{URL: "https://api.test/v1?token=s3cret-token"}, Secrets: map[string]string{"TOKEN": "s3cret-token"}}
	ret, err := result(r, outcome(), files{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if api := ret["req"].(map[string]any)["api"]; api != "https://api.test/v1?token=<secret:TOKEN>" {
		t.Errorf("req.api %v", api)
	}
}

func TestHTTPTarget(t *testing.T) {
	caller := sdk.NewCaller(sdk.Call{}, "")
	t.Setenv(sdk.ExecutableEnv, "")
	if _, err := httpTarget(caller, nil, httpReq{URL: "http://a.test/"}); err == nil || !strings.Contains(err.Error(), "needs Probe v1.37.0 or later") {
		t.Errorf("without %s: err %v", sdk.ExecutableEnv, err)
	}

	t.Setenv(sdk.ExecutableEnv, "/usr/local/bin/probe")
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
	o.Targets = []scenario.TargetState{{Kind: "http", Method: "GET", URL: "http://a.test/v1/orders/7", Status: 200, Response: "GET http://a.test/v1/orders/7 -> 200 OK\n\n{\"id\":7}"}}
	f, err := ev.write(req{Task: "t", Expect: "e"}, o)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(f.Report)
	if want := "## Last response of the API\n\nGET <http://a.test/v1/orders/7> → 200\n\n```\nGET http://a.test/v1/orders/7 -> 200 OK\n\n{\"id\":7}\n```"; !strings.Contains(string(md), want) {
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
	r := req{Task: "t", Expect: "e", IOS: &appReq{App: "com.example.app", Device: "iPhone 17"}, Model: "claude-opus-5-5", Effort: "medium", MaxSteps: 40}
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

func TestAppWith(t *testing.T) {
	actions := []map[string]any{{"name": "tap", "selector": "go"}, {"name": "tree"}}
	got := appWith(appReq{App: "com.example.app"}, actions)
	want := map[string]any{"app": "com.example.app", "actions": []any{actions[0], actions[1]}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with %v, want %v without device and timeout", got, want)
	}
	got = appWith(appReq{App: "com.example.app", Device: "iPhone 17", Timeout: 5 * time.Second}, actions)
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

func TestParse_TargetsGraphQL(t *testing.T) {
	r, err := parse(map[string]any{
		"task": "t", "expect": "e",
		"targets": map[string]any{
			"graphql": map[string]any{"url": " https://api.test/graphql ", "headers": map[string]any{"Authorization": "Bearer t", "X-Version": 2.0}, "timeout": "10s"},
			"browser": map[string]any{"url": "https://app.test/"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	g := r.GraphQL
	if g == nil || g.URL != "https://api.test/graphql" || !reflect.DeepEqual(g.Headers, map[string]string{"Authorization": "Bearer t", "X-Version": "2"}) || g.Timeout != 10*time.Second {
		t.Fatalf("graphql %+v", g)
	}
	if got := r.kinds(); !slices.Equal(got, []string{"browser", "graphql"}) {
		t.Errorf("kinds %v", got)
	}
}

func TestGraphQLWith(t *testing.T) {
	g := graphqlReq{URL: "https://api.test/graphql", Headers: map[string]string{"Authorization": "Bearer t"}, Timeout: 5 * time.Second}
	got, err := graphqlWith(g, scenario.GraphQLRequest{
		Query: "query N($id: ID!) { note(id: $id) { id } }", Variables: `{"id":"n1"}`, OperationName: "N",
		Headers: map[string]string{"X-Trace": "1", "Authorization": "mine"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"url": "https://api.test/graphql", "query": "query N($id: ID!) { note(id: $id) { id } }",
		"variables": map[string]any{"id": "n1"}, "operation_name": "N",
		"headers": map[string]any{"X-Trace": "1", "Authorization": "Bearer t"}, "timeout": "5s",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with %v, want %v", got, want)
	}
	got, err = graphqlWith(graphqlReq{URL: "http://a.test/graphql"}, scenario.GraphQLRequest{Query: "{ a }"})
	if err != nil || !reflect.DeepEqual(got, map[string]any{"url": "http://a.test/graphql", "query": "{ a }"}) {
		t.Errorf("with %v, %v", got, err)
	}
	if _, err := graphqlWith(g, scenario.GraphQLRequest{Query: "{ a }", Variables: "[1]"}); err == nil {
		t.Error("variables that are not an object were taken")
	}
}

func TestGraphQLResponse(t *testing.T) {
	got, err := graphqlResponse("graphql", &sdk.CallResult{Result: map[string]any{"res": map[string]any{
		"code": 200, "status": "200 OK", "data": map[string]any{"note": nil},
		"errors":  []any{map[string]any{"message": "no note is n9"}},
		"body":    map[string]any{"data": map[string]any{"note": nil}},
		"rawbody": `{"data":{"note":null}}`,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	want := scenario.GraphQLResponse{Code: 200, Status: "200 OK", Data: `{"note":null}`, Errors: []string{`{"message":"no note is n9"}`}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("response %+v, want %+v", got, want)
	}
	// A body that is not JSON holds no data, and is kept as it is.
	got, err = graphqlResponse("graphql", &sdk.CallResult{Result: map[string]any{"res": map[string]any{
		"code": 502, "status": "502 Bad Gateway", "data": nil, "errors": []any{}, "body": "upstream down",
	}}})
	if err != nil || !reflect.DeepEqual(got, scenario.GraphQLResponse{Code: 502, Status: "502 Bad Gateway", Body: "upstream down"}) {
		t.Errorf("response %+v, %v", got, err)
	}
	if _, err := graphqlResponse("gql", &sdk.CallResult{Result: map[string]any{}}); err == nil || err.Error() != "gql returned no res" {
		t.Errorf("err %v", err)
	}
}

func TestHostGuard(t *testing.T) {
	g := hostGuard(sdk.Guard{}, "https://api.test/graphql")
	// The guard holds no keeps of its own: those of an external action are
	// read from its action.yml.
	if !slices.Equal(g.AllowHosts, []string{"api.test:443"}) || len(g.Keeps) != 0 || g.AllowsHost("api.test:80") {
		t.Errorf("guard %+v", g)
	}
	g = hostGuard(sdk.Guard{ReadOnly: true, AllowHosts: []string{"other.test"}}, "http://api.test:8080/graphql")
	if !g.ReadOnly || !slices.Equal(g.AllowHosts, []string{"other.test"}) {
		t.Errorf("guard %+v", g)
	}
}

func TestGraphQLTarget(t *testing.T) {
	caller := sdk.NewCaller(sdk.Call{}, "")
	if _, err := graphqlTarget(caller, nil, graphqlReq{URL: "http://a.test/graphql"}); err == nil || !strings.Contains(err.Error(), "targets.graphql calls the API through probe-graphql: name it under actions of the workflow, as graphql: github.com/mozership/probe-graphql@<commit SHA>") {
		t.Errorf("without probe-graphql: err %v", err)
	}
	actions := map[string]string{"gql": "github.com/mozership/probe-graphql@" + strings.Repeat("a", 40)}
	api, err := graphqlTarget(caller, actions, graphqlReq{URL: "http://a.test/graphql", Headers: map[string]string{"X-Version": "2", "Authorization": "Bearer t"}})
	if err != nil || api.URL != "http://a.test/graphql" || !slices.Equal(api.Fixed, []string{"Authorization", "X-Version"}) || api.Call == nil {
		t.Errorf("api %+v, %v", api, err)
	}
}

func TestResult_MasksTheURLOfTheGraphQLAPI(t *testing.T) {
	r := req{Task: "t", Expect: "e", GraphQL: &graphqlReq{URL: "https://api.test/graphql?key=s3cret-key"}, Secrets: map[string]string{"KEY": "s3cret-key"}}
	ret, err := result(r, outcome(), files{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ret["req"].(map[string]any)["graphql"]; got != "https://api.test/graphql?key=<secret:KEY>" {
		t.Errorf("req.graphql %v", got)
	}
}

func TestEvidence_WritesTheLastResponseOfTheGraphQLAPI(t *testing.T) {
	ev, err := newEvidence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := outcome()
	o.Targets = []scenario.TargetState{{Kind: "graphql", URL: "http://a.test/graphql", Method: "mutation CreateNote", Status: 200, Response: "mutation CreateNote -> 200 OK\n\nData: {}"}}
	f, err := ev.write(req{Task: "t", Expect: "e"}, o)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(f.Report)
	if want := "## Last response of the GraphQL API\n\nmutation CreateNote at <http://a.test/graphql> → 200\n\n```\nmutation CreateNote -> 200 OK\n\nData: {}\n```"; !strings.Contains(string(md), want) {
		t.Errorf("the report lacks %q:\n%s", want, md)
	}
}

func TestParse_TargetsMail(t *testing.T) {
	r, err := parse(map[string]any{
		"task": "t", "expect": "e",
		"targets": map[string]any{
			"mail": map[string]any{
				"url": " https://mail.test ", "basic_auth": map[string]any{"username": "bob", "password": "pw"},
				"headers": map[string]any{"X-Tenant": 7.0}, "account_id": "A1", "timeout": "10s",
			},
			"browser": map[string]any{"url": "https://app.test/"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := &mailReq{URL: "https://mail.test", Username: "bob", Password: "pw", Headers: map[string]string{"X-Tenant": "7"}, AccountID: "A1", Timeout: 10 * time.Second, TimeoutSet: true}
	if !reflect.DeepEqual(r.Mail, want) {
		t.Fatalf("mail %+v", r.Mail)
	}
	if got := r.kinds(); !slices.Equal(got, []string{"browser", "mail"}) {
		t.Errorf("kinds %v", got)
	}
}

func TestJMAPWith(t *testing.T) {
	calls := []any{map[string]any{"method": "Email/get"}}
	got := jmapWith(mailReq{URL: "https://mail.test", Username: "bob", Password: "pw", Headers: map[string]string{"X-Tenant": "7"}, AccountID: "A1", Timeout: 5 * time.Second, TimeoutSet: true}, calls)
	want := map[string]any{
		"url": "https://mail.test", "calls": calls, "basic_auth": map[string]any{"username": "bob", "password": "pw"},
		"headers": map[string]any{"X-Tenant": "7"}, "account_id": "A1", "timeout": "5s",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with %v, want %v", got, want)
	}
	if got := jmapWith(mailReq{URL: "http://m.test"}, calls); !reflect.DeepEqual(got, map[string]any{"url": "http://m.test", "calls": calls}) {
		t.Errorf("with %v", got)
	}
	// A timeout of 0 removes the bound of probe-jmap.
	if got := jmapWith(mailReq{URL: "http://m.test", TimeoutSet: true}, calls); got["timeout"] != "0s" {
		t.Errorf("with %v", got)
	}
}

func TestMailSearchCalls(t *testing.T) {
	after := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("JST", 9*3600))
	got := mailSearchCalls(scenario.MailQuery{To: "alice@example.com", Subject: "Confirm", After: after, Limit: 20})
	b, _ := json.Marshal(got)
	want := `[{"args":{"calculateTotal":true,"filter":{"after":"2026-01-01T18:04:05Z","subject":"Confirm","to":"alice@example.com"},"limit":20,"sort":[{"isAscending":false,"property":"receivedAt"}]},"id":"query","method":"Email/query"},` +
		`{"args":{"#ids":{"path":"/ids","resultOf":"query"},"properties":["id","from","to","subject","receivedAt","preview"]},"id":"get","method":"Email/get"}]`
	if string(b) != want {
		t.Errorf("calls\n%s\nwant\n%s", b, want)
	}
	b, _ = json.Marshal(mailReadCalls("m1"))
	if !strings.Contains(string(b), `"ids":["m1"]`) || !strings.Contains(string(b), `"fetchHTMLBodyValues":true`) || !strings.Contains(string(b), `"bodyValues"`) {
		t.Errorf("read calls %s", b)
	}
	// The read does not change the properties of the search.
	if len(mailSummaryProps) != 6 {
		t.Errorf("props %v", mailSummaryProps)
	}
}

func TestJMAPResults(t *testing.T) {
	res := func(r map[string]any) *sdk.CallResult { return &sdk.CallResult{Result: map[string]any{"res": r}} }
	search, err := jmapResults("jmap", res(map[string]any{"code": 200, "status": "200 OK", "errors": []any{}, "results": map[string]any{
		"query": map[string]any{"ids": []any{"m2", "m1"}, "total": 5},
		"get": map[string]any{"list": []any{
			map[string]any{"id": "m2", "subject": "Hi", "receivedAt": "2026-01-02T03:04:05Z", "preview": "p",
				"from": []any{map[string]any{"name": "Acme", "email": "no-reply@acme.test"}}, "to": []any{map[string]any{"email": "a@x.test"}, map[string]any{"name": "B"}}},
			map[string]any{"id": "m1"},
		}},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	list, err := mailList(search)
	if err != nil {
		t.Fatal(err)
	}
	want := scenario.MailList{Total: 5, Mails: []scenario.MailSummary{
		{ID: "m2", Subject: "Hi", Preview: "p", Received: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), From: []string{"Acme <no-reply@acme.test>"}, To: []string{"a@x.test", "B"}},
		{ID: "m1"},
	}}
	if !reflect.DeepEqual(list, want) {
		t.Errorf("list %+v\nwant %+v", list, want)
	}

	if _, err := jmapResults("jmap", res(map[string]any{"code": 200, "errors": []any{map[string]any{"kind": "method", "type": "unknownMethod"}}})); err == nil || !strings.Contains(err.Error(), `answered with errors: [{"kind":"method","type":"unknownMethod"}]`) {
		t.Errorf("err %v", err)
	}
	if _, err := jmapResults("jmap", res(map[string]any{"code": 401, "status": "401 Unauthorized"})); err == nil || err.Error() != "the mail server answered 401 Unauthorized" {
		t.Errorf("err %v", err)
	}
	// A page of a sign-in, answered with 200, is no JMAP.
	signIn := &sdk.CallResult{Result: map[string]any{"status": 1, "res": map[string]any{"code": 200, "status": "200 OK", "errors": []any{}, "results": map[string]any{}, "body": "<html>Sign in</html>"}}}
	if _, err := jmapResults("jmap", signIn); err == nil || err.Error() != "the mail server answered 200 OK, but with no JMAP method responses" {
		t.Errorf("err %v", err)
	}
	if _, err := mailList(map[string]any{"get": map[string]any{}}); err == nil {
		t.Error("a search without Email/query was taken")
	}
	if _, err := jmapResults("jm", &sdk.CallResult{Result: map[string]any{}}); err == nil || err.Error() != "jm returned no res" {
		t.Errorf("err %v", err)
	}
}

func TestMailMessage(t *testing.T) {
	read := map[string]any{"get": map[string]any{"list": []any{map[string]any{
		"id": "m1", "subject": "Hi",
		"textBody":   []any{map[string]any{"partId": "1", "type": "text/plain"}},
		"htmlBody":   []any{map[string]any{"partId": "2", "type": "text/html"}},
		"bodyValues": map[string]any{"1": map[string]any{"value": "Open https://a.test"}, "2": map[string]any{"value": "<a href=\"https://a.test\">Open</a>"}},
	}}}}
	m, err := mailMessage(read, "m1")
	if err != nil || m.ID != "m1" || m.Text != "Open https://a.test" || m.HTML != `<a href="https://a.test">Open</a>` {
		t.Errorf("mail %+v, %v", m, err)
	}
	// A mail of HTML alone has it as its text body as well, and one of text
	// alone its text as its HTML body, each read by its type.
	html := map[string]any{"partId": "2", "type": "text/html"}
	read = map[string]any{"get": map[string]any{"list": []any{map[string]any{
		"id": "m2", "textBody": []any{html}, "htmlBody": []any{html},
		"bodyValues": map[string]any{"2": map[string]any{"value": "<p>Hi</p>"}},
	}}}}
	if m, err := mailMessage(read, "m2"); err != nil || m.Text != "" || m.HTML != "<p>Hi</p>" {
		t.Errorf("mail %+v, %v", m, err)
	}
	plain := map[string]any{"partId": "1", "type": "text/plain"}
	read = map[string]any{"get": map[string]any{"list": []any{map[string]any{
		"id": "m3", "textBody": []any{plain}, "htmlBody": []any{plain},
		"bodyValues": map[string]any{"1": map[string]any{"value": "Open <https://a.test>"}},
	}}}}
	if m, err := mailMessage(read, "m3"); err != nil || m.Text != "Open <https://a.test>" || m.HTML != "" {
		t.Errorf("mail %+v, %v", m, err)
	}
	if _, err := mailMessage(map[string]any{"get": map[string]any{"list": []any{}}}, "m9"); err == nil || err.Error() != "the mailbox has no mail m9" {
		t.Errorf("err %v", err)
	}
}

func TestMailTarget(t *testing.T) {
	caller := sdk.NewCaller(sdk.Call{}, "")
	if _, err := mailTarget(caller, nil, mailReq{URL: "http://mail.test"}); err == nil || !strings.Contains(err.Error(), "targets.mail reads the mailbox through probe-jmap: name it under actions of the workflow, as jmap: github.com/mozership/probe-jmap@<commit SHA>") {
		t.Errorf("without probe-jmap: err %v", err)
	}
	actions := map[string]string{"mail": "github.com/mozership/probe-jmap@" + strings.Repeat("a", 40)}
	box, err := mailTarget(caller, actions, mailReq{URL: "http://mail.test"})
	if err != nil || box.URL != "http://mail.test" || box.Search == nil || box.Read == nil {
		t.Errorf("box %+v, %v", box, err)
	}
}

func TestResult_MasksTheURLOfTheMailbox(t *testing.T) {
	r := req{Task: "t", Expect: "e", Mail: &mailReq{URL: "https://mail.test/jmap?key=s3cret-key", Password: "pw"}, Secrets: map[string]string{"KEY": "s3cret-key"}}
	ret, err := result(r, outcome(), files{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := ret["req"].(map[string]any)
	if got["mail"] != "https://mail.test/jmap?key=<secret:KEY>" {
		t.Errorf("req.mail %v", got["mail"])
	}
}

func TestEvidence_WritesTheLastReadOfTheMailbox(t *testing.T) {
	ev, err := newEvidence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := outcome()
	o.Targets = []scenario.TargetState{{Kind: "mail", URL: "http://mail.test", Response: "From: a\nSubject: Hi"}, {Kind: "mail", URL: "http://other.test"}}
	f, err := ev.write(req{Task: "t", Expect: "e"}, o)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(f.Report)
	for _, want := range []string{"## Last read of the mailbox\n\nAt <http://mail.test>:\n\n```\nFrom: a\nSubject: Hi\n```", "Nothing was read at <http://other.test>."} {
		if !strings.Contains(string(md), want) {
			t.Errorf("the report lacks %q:\n%s", want, md)
		}
	}
}

func TestParse_TargetsDB(t *testing.T) {
	r, err := parse(map[string]any{
		"task": "t", "expect": "e",
		"targets": map[string]any{
			"db":      map[string]any{"dsn": " postgresql://reader:pw@db.test/shop ", "timeout": "10s"},
			"browser": map[string]any{"url": "https://app.test/"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := &dbReq{DSN: "postgresql://reader:pw@db.test/shop", Driver: "postgres", Timeout: 10 * time.Second}
	if !reflect.DeepEqual(r.DB, want) {
		t.Fatalf("db %+v", r.DB)
	}
	if got := r.kinds(); !slices.Equal(got, []string{"browser", "db"}) {
		t.Errorf("kinds %v", got)
	}
	for dsn, driver := range map[string]string{"mysql://u@h/d": "mysql", "file:./a.db": "sqlite", "postgres://h/d": "postgres", "redis://h": ""} {
		if got := dbDriverOf(dsn); got != driver {
			t.Errorf("driver of %s: %q, want %q", dsn, got, driver)
		}
	}
	for with, want := range map[string]map[string]any{
		"targets.db.dsn is required":                 {"timeout": "5s"},
		"targets.db.dsn must start with postgres://": {"dsn": "redis://h"},
		"targets.db.dsn must be a string":            {"dsn": 1.0},
		"targets.db.timeout: invalid timeout 0":      {"dsn": "file:a.db", "timeout": "0"},
		"unknown parameter targets.db.query":         {"dsn": "file:a.db", "query": "SELECT 1"},
	} {
		targets := map[string]any{"db": want}
		w := map[string]any{"task": "t", "expect": "e", "targets": targets}
		if _, err := parse(w); err == nil || !strings.Contains(err.Error(), with) {
			t.Errorf("%v: err %v, want %q", want, err, with)
		}
	}
}

func TestDBGuard(t *testing.T) {
	g := dbGuard(sdk.Guard{AllowHosts: []string{"db.test"}})
	if !g.ReadOnly || !g.Runs("db") || !slices.Equal(g.AllowHosts, []string{"db.test"}) {
		t.Errorf("guard %+v", g)
	}
	// The guard of the run is left as it was.
	run := sdk.Guard{}
	_ = dbGuard(run)
	if run.ReadOnly || run.Keeps != nil {
		t.Errorf("the guard of the run %+v", run)
	}
}

func TestDBResult(t *testing.T) {
	got := dbResult(map[string]any{"code": 0, "rows_affected": 1, "rows": []any{map[string]any{"id": 1.0, "status": "paid"}, "not a row"}})
	if !reflect.DeepEqual(got, scenario.DBResult{Rows: []map[string]any{{"id": 1.0, "status": "paid"}}}) {
		t.Errorf("result %+v", got)
	}
	if got := dbResult(map[string]any{"code": 1, "rows": []any{}, "error": `pq: relation "x" does not exist`}); got.Error != `pq: relation "x" does not exist` || got.Rows != nil {
		t.Errorf("result %+v", got)
	}
	if got := dbResult(map[string]any{"code": 1}); got.Error != "the statement failed" {
		t.Errorf("result %+v", got)
	}
}

func TestDBTables(t *testing.T) {
	got := dbTables([]map[string]any{
		{"table_name": "orders", "column_name": "id", "data_type": "INTEGER"},
		{"table_name": "orders", "column_name": "note", "data_type": nil},
		{"table_name": "users", "column_name": "email", "data_type": "TEXT"},
	})
	want := []scenario.DBTable{
		{Name: "orders", Columns: [][2]string{{"id", "INTEGER"}, {"note", ""}}},
		{Name: "users", Columns: [][2]string{{"email", "TEXT"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tables %+v", got)
	}
	for driver, q := range tablesQuery {
		if strings.Contains(q, ";") || !strings.HasPrefix(q, "SELECT ") {
			t.Errorf("the query of %s would be refused under read-only: %s", driver, q)
		}
	}
}

func TestDSNShown(t *testing.T) {
	for dsn, want := range map[string]string{
		"postgres://reader:s3cret@db.test:5432/shop?sslmode=disable": "postgres://reader:***@db.test:5432/shop?sslmode=disable",
		"mysql://root@db.test/shop":                                  "mysql://root@db.test/shop",
		"postgres://db.test/shop?user=r&password=s3cret":             "postgres://db.test/shop?password=***&user=r",
		"file:./data/app.db":                                         "file:./data/app.db",
		"postgres://db.test/shop?application_name=a%20b&sslmode=off": "postgres://db.test/shop?application_name=a%20b&sslmode=off",
		"postgres://u:s3cr/et@h/d":                                   "postgres://***@h/d",
		"mysql://u:pw@tcp(h:3306)/d":                                 "mysql://***@tcp(h:3306)/d",
		"postgres://u:p w/x":                                         "(a DSN that is not a URL)",
	} {
		if got := dsnShown(dsn); got != want {
			t.Errorf("%s: %s, want %s", dsn, got, want)
		}
	}
}

func TestDBTarget(t *testing.T) {
	t.Setenv(sdk.ExecutableEnv, "/usr/bin/probe")
	caller := sdk.NewCaller(sdk.Call{}, "")
	if _, err := dbTarget(caller, map[string]string{"db": "github.com/x/y@" + strings.Repeat("a", 40)}, dbReq{DSN: "file::memory:", Driver: "sqlite"}); err == nil || !strings.Contains(err.Error(), "give the name db to") {
		t.Errorf("err %v", err)
	}
	db, err := dbTarget(caller, nil, dbReq{DSN: "postgres://r:pw@db.test/shop", Driver: "postgres"})
	if err != nil || db.Name != "postgres://r:***@db.test/shop" || db.Driver != "postgres" || db.Query == nil || db.Tables == nil {
		t.Errorf("db %+v, %v", db, err)
	}
}

func TestResult_HidesThePasswordOfTheDatabase(t *testing.T) {
	r := req{Task: "t", Expect: "e", DB: &dbReq{DSN: "postgres://r:pw@db.test/shop?key=s3cret-key"}, Secrets: map[string]string{"KEY": "s3cret-key"}}
	ret, err := result(r, outcome(), files{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ret["req"].(map[string]any)["db"]; got != "postgres://r:***@db.test/shop?key=<secret:KEY>" {
		t.Errorf("req.db %v", got)
	}
}

func TestEvidence_WritesTheLastStatementOnTheDatabase(t *testing.T) {
	ev, err := newEvidence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := outcome()
	o.Targets = []scenario.TargetState{{Kind: "db", URL: "file:a.db", Query: "SELECT 1", Response: "1 row:\n{\"1\":1}"}, {Kind: "db", URL: "file:b.db"}}
	f, err := ev.write(req{Task: "t", Expect: "e"}, o)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(f.Report)
	for _, want := range []string{"## Last statement on the database\n\nOn file:a.db:\n\n```sql\nSELECT 1\n```\n\n```\n1 row:\n{\"1\":1}\n```", "No statement was run on file:b.db."} {
		if !strings.Contains(string(md), want) {
			t.Errorf("the report lacks %q:\n%s", want, md)
		}
	}
}

func TestParse_RefusesADSNThatIsNotAURL(t *testing.T) {
	_, err := parse(map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"db": map[string]any{"dsn": "POSTGRES://u:s3cr/et@h/d"}}})
	if err == nil || !strings.Contains(err.Error(), "percent-encode a special character of its password") || strings.Contains(err.Error(), "s3cr") {
		t.Errorf("err %v", err)
	}
	if got := dbDriverOf("MySQL://u@h/d"); got != "mysql" {
		t.Errorf("driver %q", got)
	}
	// A scheme in upper case is given to the db action in lower case.
	for dsn, want := range map[string]string{"POSTGRESQL://r:PW@h/D?sslmode=disable": "postgresql://r:PW@h/D?sslmode=disable", "FILE:/tmp/A.db": "file:/tmp/A.db"} {
		r, err := parse(map[string]any{"task": "t", "expect": "e", "targets": map[string]any{"db": map[string]any{"dsn": dsn}}})
		if err != nil || r.DB.DSN != want {
			t.Errorf("%s: %+v, %v", dsn, r.DB, err)
		}
	}
}

func TestHideDSN(t *testing.T) {
	got := hideDSN("postgres://reader:s3cret%2Fx@db.test/shop", `failed to connect to `+"`user=reader database=shop`"+`: password "s3cret/x" refused; dsn postgres://reader:s3cret%2Fx@db.test/shop`)
	if strings.Contains(got, "s3cret") {
		t.Errorf("hidden %q", got)
	}
	if got := hideDSN("file:a.db", "no such table: x"); got != "no such table: x" {
		t.Errorf("hidden %q", got)
	}
}

func TestDBTarget_AMissingSQLiteFile(t *testing.T) {
	t.Setenv(sdk.ExecutableEnv, "/usr/bin/probe")
	caller := sdk.NewCaller(sdk.Call{}, "")
	dir := t.TempDir()
	if _, err := dbTarget(caller, nil, dbReq{DSN: "file:" + dir + "/typo.db?mode=ro", Driver: "sqlite"}); err == nil || !strings.Contains(err.Error(), "names no SQLite file at "+dir+"/typo.db") {
		t.Errorf("err %v", err)
	}
	if _, err := os.Stat(dir + "/typo.db"); err == nil {
		t.Error("the file was made")
	}
	if err := os.WriteFile(dir+"/app.db", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dbTarget(caller, nil, dbReq{DSN: "file:" + dir + "/app.db", Driver: "sqlite"}); err != nil {
		t.Errorf("err %v", err)
	}
	if _, err := dbTarget(caller, nil, dbReq{DSN: "file::memory:", Driver: "sqlite"}); err != nil {
		t.Errorf("err %v", err)
	}
}

func TestDBGuard_KeepsTheRestOfTheGuard(t *testing.T) {
	g := dbGuard(sdk.Guard{AllowActions: []string{"agent"}, Keeps: map[string][]string{"http": {sdk.KindReadOnly}}})
	if !slices.Equal(g.AllowActions, []string{"agent"}) || !slices.Equal(g.Keeps["http"], []string{sdk.KindReadOnly}) || !g.Runs("db") {
		t.Errorf("guard %+v", g)
	}
}

func TestParse_ACacheOfEachTarget(t *testing.T) {
	for name, targets := range map[string]map[string]any{
		"an API":                  {"http": map[string]any{"url": "http://a.test/api"}},
		"a browser and an API":    {"browser": map[string]any{"url": "http://a.test/"}, "http": map[string]any{"url": "http://a.test/api"}},
		"a GraphQL API":           {"graphql": map[string]any{"url": "http://a.test/graphql"}},
		"a gRPC API":              {"grpc": map[string]any{"addr": "a.test:443"}},
		"a browser and mail":      {"browser": map[string]any{"url": "http://a.test/"}, "mail": map[string]any{"url": "http://mail.test"}},
		"a browser and database":  {"browser": map[string]any{"url": "http://a.test/"}, "db": map[string]any{"dsn": "file:a.db"}},
		"an iOS app":              {"ios": map[string]any{"app": "com.example.app"}},
		"an Android app and mail": {"android": map[string]any{"app": "com.example.app"}, "mail": map[string]any{"url": "http://mail.test"}},
	} {
		if _, err := parse(map[string]any{"task": "t", "expect": "e", "cache": "c.json", "targets": targets}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
