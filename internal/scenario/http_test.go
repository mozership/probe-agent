package scenario

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mozership/probe-agent/internal/llm"
)

// fakeAPI answers the requests of the tester from a list of responses, and
// records the requests.
type fakeAPI struct {
	responses []HTTPResponse
	requests  []HTTPRequest
	// fail fails each request with it.
	fail error
}

func (f *fakeAPI) target() *HTTP {
	return &HTTP{URL: "https://api.test/v1", Fixed: []string{"Authorization"}, OpenAPI: "openapi: 3.0.3", Call: f.call}
}

func (f *fakeAPI) call(_ context.Context, req HTTPRequest) (HTTPResponse, error) {
	f.requests = append(f.requests, req)
	if f.fail != nil {
		return HTTPResponse{}, f.fail
	}
	if len(f.responses) == 0 {
		return HTTPResponse{Code: 404, Status: "404 Not Found"}, nil
	}
	r := f.responses[0]
	f.responses = f.responses[1:]
	return r, nil
}

func apiConfig() Config {
	cfg := config()
	cfg.StartURL = ""
	cfg.Task = "Order two of item 7 with the key {secret:PASSWORD}, and look at the order."
	cfg.Expect = "The order holds two of item 7."
	return cfg
}

func TestRun_CallsAnHTTPAPI(t *testing.T) {
	api := &fakeAPI{responses: []HTTPResponse{
		{Code: 201, Status: "201 Created", Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"id":"o1","key":"hunter2-long"}`, Operation: "POST /orders"},
		{Code: 200, Status: "200 OK", Body: `{"id":"o1","item":7,"quantity":2}`, Operation: "GET /orders/{id}"},
	}}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "http_request", map[string]any{
				"method": "post", "path": "/orders", "query": map[string]any{"key": "{secret:PASSWORD}"},
				"headers": map[string]any{"X-Request": "{secret:EMAIL}"}, "body": `{"item":7,"quantity":2}`,
			}),
			toolCall("2", "http_request", map[string]any{"method": "GET", "path": "orders/o1?full=1"}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Ordered."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{`"quantity":2`}})},
	}}
	o, err := Run(t.Context(), apiConfig(), Targets{HTTP: api.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass {
		t.Fatalf("verdict %s: %s", o.Verdict, o.Reason)
	}

	// The secrets are filled in as the request is sent, and a JSON body
	// goes as JSON.
	got := api.requests[0]
	if got.Method != "POST" || got.URL != "https://api.test/v1/orders?key=hunter2-long" || got.Body != `{"item":7,"quantity":2}` {
		t.Errorf("request %+v", got)
	}
	if got.Headers["X-Request"] != "alice@example.com" || got.Headers["Content-Type"] != "application/json" {
		t.Errorf("headers %v", got.Headers)
	}
	if got := api.requests[1]; got.URL != "https://api.test/v1/orders/o1?full=1" || got.Body != "" || got.Headers["Content-Type"] != "" {
		t.Errorf("request %+v", got)
	}

	tester := m.chats[0]
	if tester.opts.Browser != nil || !slices.ContainsFunc(tester.opts.Tools, func(tl llm.Tool) bool { return tl.Name == "http_request" }) {
		t.Error("http_request is not offered alone")
	}
	for _, want := range []string{"The HTTP API at https://api.test/v1, through http_request:", "fixes the headers Authorization", "<openapi>\nopenapi: 3.0.3\n</openapi>"} {
		if !strings.Contains(tester.opts.System, want) {
			t.Errorf("the system prompt lacks %q:\n%s", want, tester.opts.System)
		}
	}
	if !strings.Contains(tester.opts.Prompt, "seen in the responses of the API") || !strings.HasSuffix(tester.opts.Prompt, "No request has been made to the API at https://api.test/v1.") {
		t.Errorf("prompt:\n%s", tester.opts.Prompt)
	}

	// The model reads the response with the secrets masked, and the URL
	// with their placeholders.
	res := tester.turns[1].Results[0]
	for _, want := range []string{"POST https://api.test/v1/orders?key={secret:PASSWORD} -> 201 Created", "Content-Type: application/json", `"key":"{secret:PASSWORD}"`} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("the result lacks %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "hunter2-long") {
		t.Error("the model was shown the value of a secret")
	}

	if len(o.Steps) != 2 || o.Steps[0].Target != "http" || o.Steps[0].URL != "https://api.test/v1/orders?key={secret:PASSWORD}" || o.Steps[0].Input["headers"].(map[string]any)["X-Request"] != "{secret:EMAIL}" {
		t.Errorf("steps %+v", o.Steps)
	}
	want := TargetState{Kind: "http", Method: "GET", URL: "https://api.test/v1/orders/o1?full=1", Status: 200}
	if len(o.Targets) != 1 || o.Targets[0] != want || o.URL != "" {
		t.Errorf("targets %+v, url %q", o.Targets, o.URL)
	}
	judge := m.chats[1].opts.Prompt
	if !strings.Contains(judge, "The last response of the HTTP API https://api.test/v1:\n<last_response>\nGET https://api.test/v1/orders/o1?full=1 -> 200 OK") {
		t.Errorf("judge prompt:\n%s", judge)
	}
	if len(o.Evidence) != 1 || !o.Evidence[0].Verified {
		t.Errorf("evidence %+v", o.Evidence)
	}
}

func TestRun_HTTPRequestsRefused(t *testing.T) {
	api := &fakeAPI{}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "http_request", map[string]any{"method": "GET", "path": "https://evil.test/"}),
			toolCall("2", "http_request", map[string]any{"method": "GET", "path": "//evil.test/x"}),
			toolCall("3", "http_request", map[string]any{"method": "GET", "path": "/../admin"}),
			toolCall("4", "http_request", map[string]any{"method": "GET", "path": "/x", "headers": map[string]any{"authorization": "mine"}}),
			toolCall("5", "http_request", map[string]any{"method": "TRACE", "path": "/x"}),
			toolCall("6", "http_request", map[string]any{"method": "GET", "path": "/x", "body": "{secret:NOPE}"}),
			toolCall("7", "finish", map[string]any{"status": "done", "summary": "Done."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "fail", "reason": "r", "evidence": []string{}})},
	}}
	o, err := Run(t.Context(), apiConfig(), Targets{HTTP: api.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.requests) != 0 {
		t.Errorf("sent %+v", api.requests)
	}
	wants := []string{"names a host", "names a host", "leads out of the base URL", "the header authorization is fixed by the workflow", "the method \"TRACE\" must be one of", "no secret is named NOPE"}
	for i, want := range wants {
		res := m.chats[0].turns[i+1].Results[0]
		if !res.IsError || !strings.Contains(res.Text, want) {
			t.Errorf("call %d: %+v, want an error that says %q", i+1, res, want)
		}
		if o.Steps[i].Error == "" {
			t.Errorf("step %d records no error", i+1)
		}
	}
	if o.Targets[0] != (TargetState{Kind: "http"}) || !strings.Contains(m.chats[1].opts.Prompt, "<last_response>\nNo request was made.\n</last_response>") {
		t.Errorf("targets %+v, judge prompt:\n%s", o.Targets, m.chats[1].opts.Prompt)
	}
}

func TestRun_HTTPRequestThatGetsNoResponse(t *testing.T) {
	api := &fakeAPI{fail: errors.New("the action http does not keep to read-only, which the run is under")}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "http_request", map[string]any{"method": "POST", "path": "/orders"}),
			toolCall("2", "finish", map[string]any{"status": "blocked", "summary": "Refused."}),
		},
	}}
	o, err := Run(t.Context(), apiConfig(), Targets{HTTP: api.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if res := m.chats[0].turns[1].Results[0]; !res.IsError || !strings.Contains(res.Text, "read-only") {
		t.Errorf("result %+v", res)
	}
	if o.Verdict != Blocked {
		t.Errorf("verdict %s", o.Verdict)
	}
}

func TestResolve(t *testing.T) {
	for base, cases := range map[string]map[string]string{
		"https://api.test/v1": {
			"/orders":       "https://api.test/v1/orders",
			"orders/":       "https://api.test/v1/orders/",
			"":              "https://api.test/v1",
			"/a/../b":       "https://api.test/v1/b",
			"/x?a=1&b=2#f":  "https://api.test/v1/x?a=1&b=2",
			"/x?q=a%20b":    "https://api.test/v1/x?q=a+b",
			"/../v1/orders": "https://api.test/v1/orders",
		},
		"http://api.test:8080/?tenant=t": {
			"/":   "http://api.test:8080/?tenant=t",
			"/x":  "http://api.test:8080/x?tenant=t",
			"/..": "http://api.test:8080/?tenant=t",
		},
	} {
		d, err := newHTTPDriver(newLedger(Config{}), HTTP{URL: base})
		if err != nil {
			t.Fatal(err)
		}
		for p, want := range cases {
			got, err := d.resolve(p, nil)
			if err != nil || got != want {
				t.Errorf("%s + %q = %q, %v; want %q", base, p, got, err, want)
			}
		}
	}
	d, _ := newHTTPDriver(newLedger(Config{}), HTTP{URL: "https://api.test/v1"})
	for _, p := range []string{"/../v2", "..", "http://api.test/v1", "//api.test/v1"} {
		if got, err := d.resolve(p, nil); err == nil {
			t.Errorf("%q resolved to %q", p, got)
		}
	}
	if got, _ := d.resolve("/x?a=1", map[string]string{"a": "2", "b": "3"}); got != "https://api.test/v1/x?a=2&b=3" {
		t.Errorf("query %q", got)
	}
}

// A response the OpenAPI document does not allow is a finding of a persona
// test without the reviewer, and the tester's request that breaks it is
// not.
func TestRun_ContractViolationsOfAPersonaTest(t *testing.T) {
	bad := Violation{In: "response", Field: "$.id", Reason: "type", Message: "id must be a string"}
	api := &fakeAPI{responses: []HTTPResponse{
		{Code: 201, Status: "201 Created", Body: `{"id":1}`, Operation: "POST /orders", Violations: []Violation{bad, {In: "request", Field: "$.item", Message: "item is required"}}},
		{Code: 201, Status: "201 Created", Body: `{"id":2}`, Operation: "POST /orders", Violations: []Violation{bad}},
	}}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "http_request", map[string]any{"method": "POST", "path": "/orders", "body": "{}"}),
			toolCall("2", "http_request", map[string]any{"method": "POST", "path": "/orders", "body": `{"item":7}`}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Ordered."}),
		},
		{toolCall("r1", "review", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{`{"id":2}`}, "findings": []any{}})},
	}}
	cfg := apiConfig()
	cfg.Task, cfg.Expect = "", ""
	cfg.Persona, cfg.Goal = "A shop that orders through the API.", "Order item 7."
	o, err := Run(t.Context(), cfg, Targets{HTTP: api.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if res := m.chats[0].turns[1].Results[0]; !strings.Contains(res.Text, "The OpenAPI document does not allow:\n- in the response, $.id: id must be a string (type)\n- in the request, $.item: item is required") {
		t.Errorf("the tester is not told of the violations:\n%s", res.Text)
	}
	review := m.chats[1].opts
	if !strings.Contains(review.Prompt, "<contract_violations>\n- step 1, POST https://api.test/v1/orders: in the response, $.id: id must be a string (type)\n- step 1, POST https://api.test/v1/orders: in the request, $.item: item is required\n- step 2,") {
		t.Errorf("review prompt:\n%s", review.Prompt)
	}
	if !strings.Contains(review.System, "they are findings already") {
		t.Errorf("review system:\n%s", review.System)
	}
	if len(o.Findings) != 1 {
		t.Fatalf("findings %+v, want the violations of the response as one", o.Findings)
	}
	f := o.Findings[0]
	if f.Title != "The response of POST /orders breaks the OpenAPI document at $.id" || !f.Verified || !slices.Equal(f.Steps, []int{1, 2}) || f.Observed != "id must be a string (type)" || f.URL != "https://api.test/v1/orders" {
		t.Errorf("finding %+v", f)
	}
}

func TestRun_ContractViolationsOfAScenarioTest(t *testing.T) {
	api := &fakeAPI{responses: []HTTPResponse{
		{Code: 200, Status: "200 OK", Body: `{"id":1}`, Violations: []Violation{{In: "response", Message: "status 200 is not declared"}}},
	}}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "http_request", map[string]any{"method": "GET", "path": "/orders/1"}),
			toolCall("2", "finish", map[string]any{"status": "done", "summary": "Read."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "fail", "reason": "r", "evidence": []string{"status 200 is not declared"}})},
	}}
	o, err := Run(t.Context(), apiConfig(), Targets{HTTP: api.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if judge := m.chats[1].opts.Prompt; !strings.Contains(judge, "<contract_violations>\n- step 1, GET https://api.test/v1/orders/1: in the response: status 200 is not declared\n</contract_violations>") {
		t.Errorf("judge prompt:\n%s", judge)
	}
	if o.Findings != nil {
		t.Errorf("a scenario test has findings %+v", o.Findings)
	}
}

func TestNewHTTPDriver_RefusesABadURL(t *testing.T) {
	if _, err := Run(t.Context(), apiConfig(), Targets{HTTP: &HTTP{URL: "ftp://a.test/"}}, &fakeModel{}); err == nil {
		t.Fatal("an API of an ftp URL was taken")
	}
}

// A secret is escaped where it stands: in the path, the query or a JSON
// string, so that its value cannot change the request around it.
func TestRun_HTTPSecretsEscaped(t *testing.T) {
	api := &fakeAPI{}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "http_request", map[string]any{
				"method": "POST", "path": "/keys/{secret:KEY}?token={secret:TOKEN}",
				"body": `{"password":"{secret:PW}"}`,
			}),
			toolCall("2", "finish", map[string]any{"status": "done", "summary": "Done."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "fail", "reason": "r", "evidence": []string{}})},
	}}
	cfg := apiConfig()
	cfg.Secrets = map[string]string{"KEY": "abc?def/x", "TOKEN": "a&role=admin", "PW": `p"w\x`}
	o, err := Run(t.Context(), cfg, Targets{HTTP: api.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	got := api.requests[0]
	if got.URL != "https://api.test/v1/keys/abc%3Fdef%2Fx?token=a%26role%3Dadmin" {
		t.Errorf("url %s", got.URL)
	}
	if got.Body != `{"password":"p\"w\\x"}` || got.Headers["Content-Type"] != "application/json" {
		t.Errorf("body %s, headers %v", got.Body, got.Headers)
	}
	if o.Steps[0].URL != "https://api.test/v1/keys/{secret:KEY}?token={secret:TOKEN}" {
		t.Errorf("step url %s", o.Steps[0].URL)
	}
}

func TestResolve_Escapes(t *testing.T) {
	d, _ := newHTTPDriver(newLedger(Config{}), HTTP{URL: "https://api.test/v1"})
	if got, err := d.resolve("/users/alice%40example.com", nil); err != nil || got != "https://api.test/v1/users/alice%40example.com" {
		t.Errorf("got %q, %v; want the escape kept", got, err)
	}
	for _, p := range []string{"/%2e%2e/admin", "/x/%2E%2E/%2e%2e/admin", "/bad%zz"} {
		if got, err := d.resolve(p, nil); err == nil {
			t.Errorf("%q resolved to %q", p, got)
		}
	}
}

// A secret in the base URL is masked wherever the model or the record sees
// it.
func TestRun_HTTPBaseURLMasked(t *testing.T) {
	api := &fakeAPI{responses: []HTTPResponse{{Code: 200, Status: "200 OK", Body: "ok"}}}
	target := api.target()
	target.URL = "https://api.test/v1?tenant=hunter2-long"
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "http_request", map[string]any{"method": "GET", "path": "/x"}),
			toolCall("2", "finish", map[string]any{"status": "done", "summary": "Done."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{"ok"}})},
	}}
	o, err := Run(t.Context(), apiConfig(), Targets{HTTP: target}, m)
	if err != nil {
		t.Fatal(err)
	}
	if api.requests[0].URL != "https://api.test/v1/x?tenant=hunter2-long" {
		t.Errorf("sent %s", api.requests[0].URL)
	}
	for _, c := range m.chats {
		for _, text := range []string{c.opts.System, c.opts.Prompt} {
			if strings.Contains(text, "hunter2-long") {
				t.Errorf("the model was shown the secret:\n%s", text)
			}
		}
		for _, turn := range c.turns {
			for _, r := range turn.Results {
				if strings.Contains(r.Text, "hunter2-long") {
					t.Errorf("the model was shown the secret:\n%s", r.Text)
				}
			}
		}
	}
	if strings.Contains(o.Targets[0].URL, "hunter2-long") || strings.Contains(o.Steps[0].URL, "hunter2-long") {
		t.Errorf("targets %+v, steps %+v", o.Targets, o.Steps)
	}
}

// A secret in a body cut at its end is masked before it is cut.
func TestRun_HTTPMasksBeforeCutting(t *testing.T) {
	body := strings.Repeat("x", maxText-80) + "hunter2-long" + strings.Repeat("y", 200)
	api := &fakeAPI{responses: []HTTPResponse{{Code: 200, Status: "200 OK", Body: body}}}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "http_request", map[string]any{"method": "GET", "path": "/x"}),
			toolCall("2", "finish", map[string]any{"status": "done", "summary": "Done."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "fail", "reason": "r", "evidence": []string{}})},
	}}
	if _, err := Run(t.Context(), apiConfig(), Targets{HTTP: api.target()}, m); err != nil {
		t.Fatal(err)
	}
	res := m.chats[0].turns[1].Results[0].Text
	if strings.Contains(res, "hunter2") || !strings.Contains(res, "{secret:PASSWORD}") || len([]rune(res)) > maxText+1 {
		t.Errorf("the result is not masked whole and cut: …%s", res[len(res)-120:])
	}
}

// The violations come before a long body, which would cut them off.
func TestResponseText_ViolationsBeforeTheBody(t *testing.T) {
	text := responseText("GET", "https://api.test/v1/x", HTTPResponse{
		Code: 200, Status: "200 OK", Body: strings.Repeat("x", 2*maxText),
		Violations: []Violation{{In: "response", Field: "$.id", Message: "bad"}},
	})
	if !strings.Contains(truncate(text, maxText), "in the response, $.id: bad") {
		t.Error("the violations are cut off with the body")
	}
}

// A response the document has no operation for, as of a path the tester
// got wrong, is no finding; the violations of one field are one finding
// whatever their reasons; and the findings stand when the tester refused.
func TestRun_ContractFindingsOfAPersonaThatRefused(t *testing.T) {
	api := &fakeAPI{responses: []HTTPResponse{
		{Code: 404, Status: "404 Not Found", Violations: []Violation{{In: "response", Message: "GET /typo is not in the document"}}},
		{Code: 200, Status: "200 OK", Body: `{"count":"1"}`, Operation: "GET /notes", Violations: []Violation{{In: "response", Field: "$.count", Reason: "type", Message: "count is wrong"}}},
		{Code: 200, Status: "200 OK", Body: `{"count":-1}`, Operation: "GET /notes", Violations: []Violation{{In: "response", Field: "$.count", Reason: "minimum", Message: "count is too small"}}},
	}}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "http_request", map[string]any{"method": "GET", "path": "/typo"}),
			toolCall("2", "http_request", map[string]any{"method": "GET", "path": "/notes"}),
			toolCall("3", "http_request", map[string]any{"method": "GET", "path": "/notes"}),
			{Stop: llm.StopRefusal, Refusal: "no"},
		},
	}}
	cfg := apiConfig()
	cfg.Task, cfg.Expect = "", ""
	cfg.Persona, cfg.Goal = "A client.", "Count the notes."
	o, err := Run(t.Context(), cfg, Targets{HTTP: api.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Stopped != StoppedRefused {
		t.Fatalf("stopped %s", o.Stopped)
	}
	if len(o.Findings) != 1 || o.Findings[0].Title != "The response of GET /notes breaks the OpenAPI document at $.count" || !slices.Equal(o.Findings[0].Steps, []int{2, 3}) || len(o.Findings[0].Evidence) != 2 {
		t.Errorf("findings %+v", o.Findings)
	}
}
