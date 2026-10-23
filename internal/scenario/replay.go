package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mozership/probe-agent/internal/llm"
	"github.com/mozership/probe-agent/internal/page"
)

// recordingVersion is the version of the recordings this package writes
// and replays. It changes with the rules a recording is matched by, so
// that a recording made under other rules is made again.
const recordingVersion = 2

// pathWait bounds waiting for the page to reach the route a replayed step
// reached when it was recorded, as after a form that is slow to submit.
var pathWait = 3 * time.Second

// Recording is the browser steps of a scenario test that passed, for a
// later run to replay without asking the model.
type Recording struct {
	Version int `json:"version"`
	// Task and Expect are of the test recorded, which a recording is
	// replayed for only while they are the same.
	Task   string `json:"task"`
	Expect string `json:"expect"`
	// Start is the location of the URL the test opens first, and Landing
	// the location of the page it opened, as after a redirect, which the
	// replay checks it begins on. A location on the host of the test is
	// its path, so that a recording made on one host is replayed on
	// another.
	Start   string `json:"start"`
	Landing string `json:"landing"`
	// Viewport is the size of the viewport, which a step at a point of it
	// is replayed only at.
	Viewport Viewport   `json:"viewport"`
	Steps    []Recorded `json:"steps"`
}

// Viewport is the size of the viewport, in CSS pixels.
type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Recorded is a step of a recording.
type Recorded struct {
	Tool string `json:"tool"`
	// Input is the input of the step as the model gave it, with the
	// secrets as placeholders, the URL of a page on the host of the test
	// as a path, and no target when Locator describes it.
	Input map[string]any `json:"input,omitempty"`
	// Locator describes the element the step acted on, which the replay
	// finds again in place of the ref the model gave.
	Locator *page.Locator `json:"locator,omitempty"`
	// Path is the location of the page after the step, whose route the
	// replay checks it reaches.
	Path string `json:"path"`
	// Alerts are the alerts the page showed after the step, normalized. A
	// replay that shows another stops.
	Alerts []string `json:"alerts,omitempty"`
	// Gap says why the step cannot be replayed, as for a value the tester
	// read off the screen: the replay hands the test over there.
	Gap string `json:"gap,omitempty"`
}

// The statuses of a replay.
const (
	// ReplayMiss is a test that had no recording to replay, one of another
	// task, or one that began on another page.
	ReplayMiss = "miss"
	// ReplayHit is a test that replayed all of its recording, so that the
	// model was asked only to judge.
	ReplayHit = "hit"
	// ReplayPartial is a test whose recording stopped matching the
	// application, from where the tester carried on.
	ReplayPartial = "partial"
)

// Replay is how a test used its recording.
type Replay struct {
	// Status is miss, hit or partial.
	Status string `json:"status"`
	// Replayed is how many steps of the recording were replayed.
	Replayed int `json:"replayed"`
	// DivergedAt is the step of the recording, from 1, that could not be
	// replayed, and Reason is why; or why the recording was not used.
	DivergedAt int    `json:"diverged_at,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Stale is whether it stopped because the application no longer
	// matches the recording, rather than at a gap or a limit of the test.
	Stale bool `json:"stale,omitempty"`
}

// record keeps a step that changed the page, for the recording of the run.
// pixels is whether the tester had a screenshot of the page.
func (r *run) record(ctx context.Context, name string, input map[string]any, loc *page.Locator, at string, pixels bool) {
	in := maps.Clone(input)
	rec := Recorded{Tool: name, Input: in, Locator: loc, Path: r.relative(at), Alerts: r.alerts(ctx)}
	if loc != nil {
		// The replay finds the element by its locator, and the ref the
		// model gave means nothing to a later page.
		delete(in, "target")
	} else if targeted[name] {
		if t, _ := in["target"].(map[string]any); t["type"] == "ref" {
			rec.Gap = "the element has no name to find it again by"
		}
	}
	if u, ok := in["url"].(string); ok && name == "navigate" {
		in["url"] = r.relative(u)
	}
	for _, k := range []string{"text", "value"} {
		if v, ok := in[k].(string); ok && (name == "type" || name == "form_input") {
			instruction := r.secrets.mask(r.cfg.Task) + "\n" + r.secrets.mask(r.cfg.Expect)
			if why := derived(v, instruction, r.shown, pixels); why != "" {
				// The value is never replayed, and is this run's data, so it
				// is not kept.
				delete(in, k)
				rec.Gap = fmt.Sprintf("its value may have been read off the screen or the date (%s)", why)
			}
		}
	}
	r.recorded = append(r.recorded, rec)
}

var digitRun = regexp.MustCompile(`\d+`)

// alerts returns the alerts the page shows, masked, and with their numbers
// as #, which change from run to run.
func (r *run) alerts(ctx context.Context) []string {
	texts, err := r.page.Alerts(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, t := range texts {
		t = digitRun.ReplaceAllString(r.secrets.mask(strings.Join(strings.Fields(t), " ")), "#")
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// relative returns raw as a path when it is on the host of the start URL.
func (r *run) relative(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host != hostOf(r.cfg.StartURL) {
		return raw
	}
	u.Scheme, u.Host, u.User = "", "", nil
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

// absolute resolves a path of a recording against the start URL.
func (r *run) absolute(raw string) string {
	switch raw {
	case "back", "forward", "reload":
		return raw
	}
	base, err := url.Parse(r.cfg.StartURL)
	if err != nil {
		return raw
	}
	u, err := base.Parse(raw)
	if err != nil {
		return raw
	}
	return u.String()
}

// usable says why the recording is not for this test, or "" when it is.
func (r *run) usable(rec *Recording) string {
	switch {
	case rec.Version != recordingVersion:
		return fmt.Sprintf("the recording is of version %d, not %d", rec.Version, recordingVersion)
	case rec.Task != r.secrets.mask(r.cfg.Task) || rec.Expect != r.secrets.mask(r.cfg.Expect):
		return "the task or the expectation changed since the recording"
	case !sameRoute(rec.Start, r.relative(r.cfg.StartURL)):
		return "the start URL changed since the recording"
	case len(rec.Steps) == 0:
		return "the recording has no steps"
	}
	return ""
}

// replay replays the recording, and returns how far it got. It begins only
// on the page the recording began on, and stops at the first step that
// cannot be replayed, that does not reach the route it reached when
// recorded, or after which the page shows an alert it did not.
func (r *run) replay(ctx context.Context, rec *Recording) Replay {
	if at := r.relative(r.url); !sameRoute(rec.Landing, at) {
		return Replay{Status: ReplayMiss, Stale: true, Reason: fmt.Sprintf("the test began on %s, where the recording began on %s", at, rec.Landing)}
	}
	rp := Replay{Status: ReplayHit}
	for i, s := range rec.Steps {
		diverge := func(stale bool, reason string) Replay {
			rp.Status, rp.DivergedAt, rp.Reason, rp.Stale = ReplayPartial, i+1, reason, stale
			return rp
		}
		switch {
		case s.Gap != "":
			return diverge(false, s.Gap)
		case len(r.steps) >= r.cfg.MaxSteps:
			return diverge(false, "the test ran out of steps")
		case s.Locator == nil && targeted[s.Tool] && rec.Viewport != r.cfg.Viewport:
			return diverge(true, fmt.Sprintf("the step is at a point of a viewport of %dx%d, and this one is %dx%d",
				rec.Viewport.Width, rec.Viewport.Height, r.cfg.Viewport.Width, r.cfg.Viewport.Height))
		}
		input := maps.Clone(s.Input)
		if input == nil {
			input = map[string]any{}
		}
		if s.Locator != nil {
			ref, err := r.page.Locate(ctx, *s.Locator)
			if err != nil {
				return diverge(true, err.Error())
			}
			input["target"] = map[string]any{"type": "ref", "ref": ref}
		}
		if u, ok := input["url"].(string); ok && s.Tool == "navigate" {
			input["url"] = r.absolute(u)
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return diverge(false, err.Error())
		}
		res, step := r.browser(ctx, llm.Call{ID: fmt.Sprintf("replay_%d", i+1), Name: s.Tool, Toolset: llm.ToolsetBrowser, Input: raw})
		step.Index = len(r.steps) + 1
		step.Notes = []string{fmt.Sprintf("Replayed step %d of the recording.", i+1)}
		r.steps = append(r.steps, step)
		if res.IsError {
			return diverge(true, fmt.Sprintf("%s failed: %s", s.Tool, strings.TrimPrefix(res.Text, "Error: ")))
		}
		if at := r.reach(ctx, s.Path); !sameRoute(s.Path, at) {
			return diverge(true, fmt.Sprintf("the page went to %s, where the recording went to %s", at, s.Path))
		}
		for _, a := range r.alerts(ctx) {
			if !slices.Contains(s.Alerts, a) {
				return diverge(true, fmt.Sprintf("the page shows an alert the recording did not: %s", quote(a)))
			}
		}
		rp.Replayed++
		if ctx.Err() != nil {
			return diverge(false, "the test ran out of time")
		}
	}
	return rp
}

// reach waits a while for the page to be on the route of path, and
// returns the location it is at.
func (r *run) reach(ctx context.Context, path string) string {
	deadline := time.Now().Add(pathWait)
	for {
		u, title, err := r.page.State(ctx)
		at := r.relative(u)
		if err == nil {
			r.url, r.title = u, title
		}
		if sameRoute(path, at) || time.Now().After(deadline) || ctx.Err() != nil {
			return at
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// recording returns the recording of the run.
func (r *run) recording() *Recording {
	return &Recording{
		Version:  recordingVersion,
		Task:     r.secrets.mask(r.cfg.Task),
		Expect:   r.secrets.mask(r.cfg.Expect),
		Start:    r.relative(r.cfg.StartURL),
		Landing:  r.landing,
		Viewport: r.cfg.Viewport,
		Steps:    r.recorded,
	}
}

// sameRecording says whether two recordings would replay alike, so that a
// recording is not written again for nothing.
func sameRecording(a, b *Recording) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ja) == string(jb)
}

// handoff tells the tester what was replayed, and where the recording
// stopped, for it to carry on from there.
func (r *run) handoff(rp Replay, rec *Recording) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nThe first %d steps of the task were replayed from a recording of an earlier run:\n", rp.Replayed)
	for i, s := range rec.Steps[:rp.Replayed] {
		fmt.Fprintf(&b, "%d. %s%s\n", i+1, s.Tool, describeRecorded(s))
	}
	s := rec.Steps[rp.DivergedAt-1]
	fmt.Fprintf(&b, "The recording's next step, %s%s, could not be replayed: %s.", s.Tool, describeRecorded(s), rp.Reason)
	if rp.Stale {
		b.WriteString(" The application may have changed.")
	}
	fmt.Fprintf(&b, " Carry on with the task from the page the browser is on, %s; read it first.", r.secrets.mask(r.url))
	return b.String()
}

func describeRecorded(s Recorded) string {
	var parts []string
	if s.Locator != nil {
		parts = append(parts, "on "+s.Locator.String())
	}
	for _, k := range []string{"url", "text", "value"} {
		if v, ok := s.Input[k]; ok {
			b, _ := json.Marshal(v)
			parts = append(parts, k+" "+string(b))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, ", ")
}
