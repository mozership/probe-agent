package scenario

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/mozership/probe-agent/internal/llm"
)

// stubDriver handles the calls of the tool named after its kind, and
// records the IDs of the calls it ran.
type stubDriver struct {
	kind string
	ran  []string
}

func (d *stubDriver) Kind() string               { return d.kind }
func (d *stubDriver) Open(context.Context) error { return nil }
func (d *stubDriver) Offer(opts *llm.ChatOptions) {
	opts.Tools = append(opts.Tools, llm.Tool{Name: d.kind + "_do"})
}
func (d *stubDriver) Handles(call llm.Call) bool {
	return call.Toolset == "" && call.Name == d.kind+"_do"
}
func (d *stubDriver) Brief() string   { return "The " + d.kind + ":" }
func (d *stubDriver) Opening() string { return "The " + d.kind + " is open." }
func (d *stubDriver) Final(context.Context, bool) observation {
	return observation{heading: "The " + d.kind + " at the end:", state: TargetState{Kind: d.kind}}
}
func (d *stubDriver) Do(_ context.Context, call llm.Call) (llm.Result, Step) {
	d.ran = append(d.ran, call.ID)
	return llm.Result{CallID: call.ID, Text: "done on " + d.kind}, Step{Tool: call.Name, Result: "done on " + d.kind}
}

func TestRun_DriverOf(t *testing.T) {
	cfg := config()
	r := &run{ledger: newLedger(cfg)}
	r.web = newBrowserDriver(r.ledger, &fakePage{url: cfg.StartURL})
	ios := &stubDriver{kind: "ios"}
	r.drivers = []Driver{r.web, ios}

	tests := []struct {
		call llm.Call
		want string
	}{
		{call: llm.Call{Name: "left_click", Toolset: llm.ToolsetBrowser}, want: "browser"},
		{call: llm.Call{Name: "ios_do"}, want: "ios"},
		{call: llm.Call{Name: "left_click"}, want: ""},
		{call: llm.Call{Name: "finish"}, want: ""},
	}
	for _, tt := range tests {
		d := r.driverOf(tt.call)
		got := ""
		if d != nil {
			got = d.Kind()
		}
		if got != tt.want {
			t.Errorf("driverOf(%s, toolset %q) = %q, want %q", tt.call.Name, tt.call.Toolset, got, tt.want)
		}
	}
}

func TestBrowserDriver_Offer(t *testing.T) {
	r := newLedger(config())
	d := newBrowserDriver(r, &fakePage{})
	var opts llm.ChatOptions
	d.Offer(&opts)
	if opts.Browser == nil || len(opts.Browser.Disabled) != len(Disabled) {
		t.Errorf("browser toolset = %+v, want it offered without the members of Disabled", opts.Browser)
	}
}

func TestBrowserDriver_HostsDefaultToTheStartURL(t *testing.T) {
	cfg := config()
	cfg.Hosts = nil
	d := newBrowserDriver(newLedger(cfg), &fakePage{})
	if len(d.hosts) != 1 || d.hosts[0] != hostOf(cfg.StartURL) {
		t.Errorf("hosts = %v, want the host of %s", d.hosts, cfg.StartURL)
	}
}

// The tester is offered the tools of every driver, and each call reaches
// the driver that handles it, its result the next turn of the model, and
// its step the ledger of the test.
func TestRun_TestDispatchesToEachDriver(t *testing.T) {
	cfg := config()
	m := &fakeModel{scripts: [][]llm.Reply{{
		toolCall("c1", "ios_do", map[string]any{}),
		browserCall("c2", "read_page", map[string]any{}),
		toolCall("c3", "finish", map[string]any{"status": "done", "summary": "Done."}),
	}}}
	r := &run{ledger: newLedger(cfg), model: m}
	r.web = newBrowserDriver(r.ledger, &fakePage{url: cfg.StartURL, read: "button \"Sign up\" [ref_1]"})
	ios := &stubDriver{kind: "ios"}
	r.drivers = []Driver{r.web, ios}

	stopped, _, err := r.test(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stopped != StoppedDone {
		t.Fatalf("stopped = %s, want %s", stopped, StoppedDone)
	}

	chat := m.chats[0]
	if chat.opts.Browser == nil {
		t.Error("the browser toolset is not offered")
	}
	if !slices.ContainsFunc(chat.opts.Tools, func(tl llm.Tool) bool { return tl.Name == "ios_do" }) {
		t.Errorf("tools = %v, want the tool of the second driver among them", chat.opts.Tools)
	}
	if !slices.Equal(ios.ran, []string{"c1"}) {
		t.Errorf("the second driver ran %v, want [c1]", ios.ran)
	}

	// The turns after the first carry the results of the calls before.
	if len(chat.turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(chat.turns))
	}
	if res := chat.turns[1].Results; len(res) != 1 || res[0].CallID != "c1" || res[0].Text != "done on ios" {
		t.Errorf("results after the call of the second driver = %+v", res)
	}
	if res := chat.turns[2].Results; len(res) != 1 || res[0].CallID != "c2" || !strings.Contains(res[0].Text, "Sign up") {
		t.Errorf("results after the call of the browser = %+v", res)
	}

	if len(r.steps) != 2 {
		t.Fatalf("steps = %+v, want one for each call", r.steps)
	}
	for i, want := range []string{"ios_do", "read_page"} {
		if s := r.steps[i]; s.Index != i+1 || s.Tool != want {
			t.Errorf("step %d = %+v, want %s at index %d", i, s, want, i+1)
		}
	}
	// What the browser showed is in the ledger the judge reads.
	if !slices.ContainsFunc(r.texts, func(s string) bool { return strings.Contains(s, "Sign up") }) {
		t.Errorf("texts = %v, want what the browser showed", r.texts)
	}
}
