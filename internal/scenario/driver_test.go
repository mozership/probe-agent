package scenario

import (
	"context"
	"testing"

	"github.com/mozership/probe-agent/internal/llm"
)

// stubDriver handles the calls of the tools that start with its kind.
type stubDriver struct{ kind string }

func (d stubDriver) Kind() string               { return d.kind }
func (d stubDriver) Open(context.Context) error { return nil }
func (d stubDriver) Offer(opts *llm.ChatOptions) {
	opts.Tools = append(opts.Tools, llm.Tool{Name: d.kind + "_do"})
}
func (d stubDriver) Handles(call llm.Call) bool {
	return call.Toolset == "" && call.Name == d.kind+"_do"
}
func (d stubDriver) Do(context.Context, llm.Call) (llm.Result, Step) {
	return llm.Result{Text: d.kind}, Step{Tool: d.kind + "_do"}
}

func TestRun_DriverOf(t *testing.T) {
	cfg := config()
	r := &run{ledger: newLedger(cfg)}
	r.web = newBrowserDriver(r.ledger, &fakePage{url: cfg.StartURL})
	ios := stubDriver{kind: "ios"}
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
