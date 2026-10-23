// Package action serves the agent as a Probe action: it reads the step's
// with, starts a browser and the model, runs the test, and returns how it
// came out, with its evidence written to files.
package action

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe/actionrpc"

	"github.com/mozership/probe-agent/internal/llm"
	"github.com/mozership/probe-agent/internal/page"
	"github.com/mozership/probe-agent/internal/scenario"
)

// Action is the agent action.
type Action struct {
	log hclog.Logger
	// ctx ends when Probe stops the action, so that the browser is closed
	// rather than left running.
	ctx context.Context
}

// Serve serves the action until Probe closes the connection.
func Serve() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	actionrpc.Serve(func(log hclog.Logger) actionrpc.Action {
		return &Action{log: log, ctx: ctx}
	})
}

// Run runs the test the step asks for.
func (a *Action) Run(with map[string]any) (map[string]any, error) {
	actionrpc.LogParams(a.log, "received agent action request", forLog(with))
	ret, err := a.run(with)
	actionrpc.LogOutcome(a.log, "agent test", ret, err)
	return ret, err
}

func (a *Action) run(with map[string]any) (map[string]any, error) {
	start := time.Now()
	r, err := parse(with)
	if err != nil {
		return nil, err
	}
	var rec *scenario.Recording
	if r.Cache != "" && r.CacheMode != cacheRefresh {
		if rec, err = loadRecording(r.Cache); err != nil {
			return nil, err
		}
	}
	ev, err := newEvidence(r.EvidenceDir)
	if err != nil {
		return nil, err
	}
	chrome, err := page.Start(page.Options{Headless: r.Headless, Width: r.Width, Height: r.Height})
	if err != nil {
		return nil, err
	}
	defer chrome.Close()

	cfg := scenario.Config{
		Task:     r.Task,
		Expect:   r.Expect,
		Persona:  r.Persona,
		Goal:     r.Goal,
		StartURL: r.URL,
		Hosts:    r.AllowHosts,
		Secrets:  r.Secrets,
		MaxSteps: r.MaxSteps,
		Timeout:  r.Timeout,
		Save:     ev.save,
		// A recording is replayed in any mode of the cache but refresh,
		// and written in auto and refresh.
		Recording: rec,
		Record:    r.Cache != "" && (r.CacheMode == cacheAuto || r.CacheMode == cacheRefresh),
		Strict:    r.Cache != "" && r.CacheMode == cacheStrict,
		Viewport:  scenario.Viewport{Width: r.Width, Height: r.Height},
	}
	model := llm.NewAnthropic(r.Model, r.Effort)
	o, err := scenario.Run(a.ctx, cfg, chrome, model)
	if err != nil {
		return nil, err
	}
	a.log.Info("agent test finished", "verdict", o.Verdict, "steps", len(o.Steps), "evidence", ev.dir)

	f, err := ev.write(r, o)
	if err != nil {
		return nil, err
	}
	if o.Recording != nil {
		if err := saveRecording(r.Cache, o.Recording); err != nil {
			return nil, err
		}
		f.Recording = r.Cache
		a.log.Info("recorded the test", "cache", r.Cache, "steps", len(o.Recording.Steps))
	}
	return result(r, o, f, time.Since(start))
}

// result is what the action returns: req, res, rt and status, as Probe
// reads them. It is turned to JSON and back, so that every value has a
// type the connection to Probe carries.
func result(r req, o scenario.Outcome, f files, took time.Duration) (map[string]any, error) {
	res := map[string]any{
		"mode":           o.Mode,
		"verdict":        o.Verdict,
		"reason":         o.Reason,
		"evidence":       o.Evidence,
		"stopped":        o.Stopped,
		"summary":        o.Summary,
		"steps":          o.Steps,
		"url":            o.URL,
		"title":          o.Title,
		"console_errors": o.ConsoleErrors,
		"network_errors": o.NetworkErrors,
		"metrics":        o.Metrics,
		"usage":          o.Usage,
		"model":          o.Model,
		"filepaths": map[string]string{
			"report":     f.Report,
			"trace":      f.Trace,
			"screenshot": o.FinalScreenshot,
		},
		"report": shortReport(r, o, f),
	}
	if o.Mode == scenario.ModePersona {
		res["findings"] = o.Findings
	}
	if c := cacheOf(r, o, f.Recording != ""); c != nil {
		res["cache"] = c
	}
	reqMap := map[string]any{
		"url":       r.URL,
		"model":     r.Model,
		"effort":    r.Effort,
		"max_steps": r.MaxSteps,
		"timeout":   r.Timeout.String(),
	}
	if o.Mode == scenario.ModePersona {
		reqMap["persona"], reqMap["goal"] = r.Persona, r.Goal
	} else {
		reqMap["task"], reqMap["expect"] = r.Task, r.Expect
	}
	ret := map[string]any{"req": reqMap, "res": res, "rt": took.String(), "status": 0}
	b, err := json.Marshal(ret)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// shortReport is what Probe prints under the step: the verdict, why, and
// where the full report is.
func shortReport(r req, o scenario.Outcome, f files) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %s\n", verdictMarks[o.Verdict], o.Verdict, oneLine(o.Reason))
	for _, e := range o.Evidence {
		if e.Verified {
			fmt.Fprintf(&b, "  evidence: %q\n", e.Quote)
		}
	}
	if o.Mode == scenario.ModePersona {
		fmt.Fprintf(&b, "  %d findings\n", len(o.Findings))
		for _, f := range o.Findings {
			unverified := ""
			if !f.Verified {
				unverified = " (unverified)"
			}
			fmt.Fprintf(&b, "  %s %s: %s%s\n", severityMarks[f.Severity], f.Severity, oneLine(f.Title), unverified)
		}
	}
	if c := cacheOf(r, o, f.Recording != ""); c != nil {
		fmt.Fprintf(&b, "  cache: %s", c.Status)
		switch c.Status {
		case scenario.ReplayHit:
			fmt.Fprintf(&b, ", %d steps replayed without the model", c.Replayed)
		case scenario.ReplayPartial:
			if o.Stopped == scenario.StoppedStale {
				fmt.Fprintf(&b, ", %d steps replayed, stale at step %d: %s", c.Replayed, c.DivergedAt, c.Reason)
			} else {
				fmt.Fprintf(&b, ", %d steps replayed, then the tester from step %d: %s", c.Replayed, c.DivergedAt, c.Reason)
			}
		case scenario.ReplayMiss:
			if c.Reason != "" {
				fmt.Fprintf(&b, " (%s)", c.Reason)
			}
		}
		if c.Saved {
			fmt.Fprintf(&b, ", recorded to %s", c.Path)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "  %d steps, stopped: %s, %s\n", len(o.Steps), o.Stopped, o.Duration.Round(time.Second))
	fmt.Fprintf(&b, "  report: %s", f.Report)
	return b.String()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
