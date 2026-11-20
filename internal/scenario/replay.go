package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
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

// Recording is the steps of a scenario test that passed, for a later run
// to replay without asking the model: those of a browser, and the calls of
// the targets that answer them, as an API.
type Recording struct {
	Version int `json:"version"`
	// Task and Expect are of the test recorded, which a recording is
	// replayed for only while they are the same.
	Task   string `json:"task"`
	Expect string `json:"expect"`
	// Start is the location of the URL the browser opens first, and
	// Landing the location of the page it opened, as after a redirect,
	// which the replay checks it begins on. A location on the host of the
	// test is its path, so that a recording made on one host is replayed
	// on another. Both are empty for a test without a browser.
	Start   string `json:"start,omitempty"`
	Landing string `json:"landing,omitempty"`
	// Viewport is the size of the viewport, which a step at a point of it
	// is replayed only at.
	Viewport Viewport   `json:"viewport,omitzero"`
	Steps    []Recorded `json:"steps"`
	// Checks are what a replay of all of the steps is passed on without
	// the judge, when it meets them; a recording without them is judged.
	Checks *Checks `json:"checks,omitempty"`
}

// Checks are what the judge of the run recorded passed it on, as a
// deterministic check can tell it again: the texts it quoted of the pages
// the run ended on, and the errors those pages had.
type Checks struct {
	// Quotes are the quotes of the judge the pages the run ended on
	// showed, with the secrets as placeholders.
	Quotes []string `json:"quotes"`
	// Errors are the console errors and the failed requests of the run,
	// normalized. A replay that has another is judged.
	Errors []string `json:"errors,omitempty"`
}

// Viewport is the size of the viewport, in CSS pixels.
type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Recorded is a step of a recording.
type Recorded struct {
	// Target is the kind of the target of the step, as http; empty for the
	// browser.
	Target string `json:"target,omitempty"`
	Tool   string `json:"tool"`
	// Input is the input of the step as the model gave it, with the
	// secrets as placeholders, the URL of a page on the host of the test
	// as a path, and no target when Locator describes it.
	Input map[string]any `json:"input,omitempty"`
	// Locator describes the element the step acted on, which the replay
	// finds again in place of the ref the model gave.
	Locator *page.Locator `json:"locator,omitempty"`
	// Path is the location of the page after a step of the browser, whose
	// route the replay checks it reaches.
	Path string `json:"path,omitempty"`
	// Alerts are the alerts the page showed after the step, normalized. A
	// replay that shows another stops.
	Alerts []string `json:"alerts,omitempty"`
	// Refs are where the values of the input of a call written {answer:N}
	// were in the answers to earlier calls.
	Refs []Ref `json:"refs,omitempty"`
	// Outcome is what the call got, as the status of the response, which
	// the replay checks it gets again.
	Outcome string `json:"outcome,omitempty"`
	// Wait is whether the tester sent the call again and again until it got
	// its outcome, as to wait for a mail, which the replay does too.
	Wait bool `json:"wait,omitempty"`
	// Gap says why the step cannot be replayed, as for a value the tester
	// read off the screen: the replay hands the test over there.
	Gap string `json:"gap,omitempty"`
}

// kind is the kind of the target of the step.
func (s Recorded) kind() string {
	if s.Target == "" {
		return "browser"
	}
	return s.Target
}

// The statuses of a replay.
const (
	// ReplayMiss is a test that had no recording to replay, one of another
	// task, or one that began on another page.
	ReplayMiss = "miss"
	// ReplayHit is a test that replayed all of its recording, so that the
	// model was asked only to judge, or not at all when the replay met the
	// checks of the recording.
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
	// Checked is whether a replay of all of the recording met its checks,
	// and passed without the judge. When it did not, Reason says why the
	// judge was asked.
	Checked bool `json:"checked,omitempty"`
}

// record keeps a step that changed the page, for the recording of the run.
// pixels is whether the tester had a screenshot of the page.
func (d *browserDriver) record(ctx context.Context, name string, input map[string]any, loc *page.Locator, at string, pixels bool) {
	in := maps.Clone(input)
	rec := Recorded{Tool: name, Input: in, Locator: loc, Path: d.relative(at), Alerts: d.alerts(ctx)}
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
		in["url"] = d.relative(u)
	}
	for _, k := range []string{"text", "value"} {
		if v, ok := in[k].(string); ok && (name == "type" || name == "form_input") {
			instruction := d.secrets.mask(d.cfg.Task) + "\n" + d.secrets.mask(d.cfg.Expect)
			if why := derived(v, instruction, d.shown, pixels); why != "" {
				// The value is never replayed, and is this run's data, so it
				// is not kept.
				delete(in, k)
				rec.Gap = fmt.Sprintf("its value may have been read off the screen or the date (%s)", why)
			}
		}
	}
	d.keep(rec, "")
}

var digitRun = regexp.MustCompile(`\d+`)

// alerts returns the alerts the page shows, masked, and with their numbers
// as #, which change from run to run.
func (d *browserDriver) alerts(ctx context.Context) []string {
	texts, err := d.page.Alerts(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, t := range texts {
		t = digitRun.ReplaceAllString(d.secrets.mask(strings.Join(strings.Fields(t), " ")), "#")
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// relative returns raw as a path when it is on the host of the start URL.
func (d *browserDriver) relative(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host != hostOf(d.cfg.StartURL) {
		return raw
	}
	u.Scheme, u.Host, u.User = "", "", nil
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

// absolute resolves a path of a recording against the start URL.
func (d *browserDriver) absolute(raw string) string {
	switch raw {
	case "back", "forward", "reload":
		return raw
	}
	base, err := url.Parse(d.cfg.StartURL)
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
	case r.web != nil && !sameRoute(rec.Start, r.web.relative(r.cfg.StartURL)):
		return "the start URL changed since the recording"
	case r.web == nil && rec.Start != "":
		return "the recording is of a browser, which the test does not have"
	case len(rec.Steps) == 0:
		return "the recording has no steps"
	}
	for _, s := range rec.Steps {
		if r.driverOfKind(s.kind()) == nil {
			return "the recording uses the target " + s.kind() + ", which the test does not have"
		}
	}
	return ""
}

// replay replays the recording, and returns how far it got. A browser
// begins only on the page the recording began on. The replay stops at the
// first step that cannot be replayed: on the browser, one that does not
// reach the route it reached when recorded, or after which the page shows
// an alert it did not; on a target that answers, one that does not get the
// answer it got.
func (r *run) replay(ctx context.Context, rec *Recording) Replay {
	if d := r.web; d != nil {
		if at := d.relative(d.url); !sameRoute(rec.Landing, at) {
			return Replay{Status: ReplayMiss, Stale: true, Reason: fmt.Sprintf("the test began on %s, where the recording began on %s", at, rec.Landing)}
		}
	}
	rp := Replay{Status: ReplayHit}
	answers := make([]string, len(rec.Steps))
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
		}
		var stale bool
		var reason string
		if s.kind() == "browser" {
			stale, reason = r.web.replayStep(ctx, i, s, rec.Viewport)
		} else {
			answers[i], stale, reason = r.replayCall(ctx, i, s, answers)
		}
		if reason != "" {
			return diverge(stale, reason)
		}
		rp.Replayed++
		if ctx.Err() != nil {
			return diverge(false, "the test ran out of time")
		}
	}
	return rp
}

// replayStep replays step i of the recording on the browser, and returns
// why it stops the replay, if it does, and whether that is because the
// application no longer matches the recording.
func (d *browserDriver) replayStep(ctx context.Context, i int, s Recorded, viewport Viewport) (bool, string) {
	if s.Locator == nil && targeted[s.Tool] && viewport != d.cfg.Viewport {
		return true, fmt.Sprintf("the step is at a point of a viewport of %dx%d, and this one is %dx%d",
			viewport.Width, viewport.Height, d.cfg.Viewport.Width, d.cfg.Viewport.Height)
	}
	input := maps.Clone(s.Input)
	if input == nil {
		input = map[string]any{}
	}
	if s.Locator != nil {
		ref, err := d.page.Locate(ctx, *s.Locator)
		if err != nil {
			return true, err.Error()
		}
		input["target"] = map[string]any{"type": "ref", "ref": ref}
	}
	if u, ok := input["url"].(string); ok && s.Tool == "navigate" {
		input["url"] = d.absolute(u)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return false, err.Error()
	}
	res, step := d.Do(ctx, llm.Call{ID: fmt.Sprintf("replay_%d", i+1), Name: s.Tool, Toolset: llm.ToolsetBrowser, Input: raw})
	step.Index = len(d.steps) + 1
	step.Notes = []string{fmt.Sprintf("Replayed step %d of the recording.", i+1)}
	d.steps = append(d.steps, step)
	if res.IsError {
		return true, fmt.Sprintf("%s failed: %s", s.Tool, strings.TrimPrefix(res.Text, "Error: "))
	}
	if at := d.reach(ctx, s.Path); !sameRoute(s.Path, at) {
		return true, fmt.Sprintf("the page went to %s, where the recording went to %s", at, s.Path)
	}
	for _, a := range d.alerts(ctx) {
		if !slices.Contains(s.Alerts, a) {
			return true, fmt.Sprintf("the page shows an alert the recording did not: %s", quote(a))
		}
	}
	return false, ""
}

// reach waits a while for the page to be on the route of path, and
// returns the location it is at.
func (d *browserDriver) reach(ctx context.Context, path string) string {
	deadline := time.Now().Add(pathWait)
	for {
		u, title, err := d.page.State(ctx)
		at := d.relative(u)
		if err == nil {
			d.url, d.title = u, title
		}
		if sameRoute(path, at) || time.Now().After(deadline) || ctx.Err() != nil {
			return at
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// recording returns the recording of the run.
func (r *run) recording() *Recording {
	rec := &Recording{
		Version: recordingVersion,
		Task:    r.secrets.mask(r.cfg.Task),
		Expect:  r.secrets.mask(r.cfg.Expect),
		Steps:   r.recorded,
	}
	if d := r.web; d != nil {
		rec.Start, rec.Landing, rec.Viewport = d.relative(d.cfg.StartURL), d.landing, d.cfg.Viewport
	}
	return rec
}

// checks returns the checks of a run its judge passed with evidence, or
// nil when the judge quoted nothing a replay is sure to show again: the
// text of the targets last, and the answers to the calls recorded.
func (r *run) checks(evidence []Evidence, f []observation) *Checks {
	seen := r.checkText(f)
	quotes := []string{}
	for _, e := range evidence {
		q := r.secrets.mask(e.Quote)
		if e.Verified && checkNorm(q) != "" && strings.Contains(seen, checkNorm(q)) && !slices.Contains(quotes, q) {
			quotes = append(quotes, q)
		}
	}
	if len(quotes) == 0 {
		return nil
	}
	slices.Sort(quotes)
	return &Checks{Quotes: quotes, Errors: r.errorKinds()}
}

// unmet says which of the checks of a recording the run does not meet, or
// "" when it meets them all.
func (r *run) unmet(c *Checks, f []observation) string {
	if c == nil || len(c.Quotes) == 0 {
		return "the recording has no checks to pass it on"
	}
	seen := r.checkText(f)
	for _, q := range c.Quotes {
		if !strings.Contains(seen, checkNorm(q)) {
			return fmt.Sprintf("the targets do not show %s, which the judge of the recorded run quoted", quote(q))
		}
	}
	for _, e := range r.errorKinds() {
		if !slices.Contains(c.Errors, e) {
			return "the run had an error the recorded run did not: " + e
		}
	}
	return ""
}

// checkText is what the checks of a recording are checked against: the
// text the targets showed last, and the answers to the calls recorded,
// normalized. The elements of a page are left out, as they hold what its
// fields were filled with, which a quote of the task would find on a form
// that was never sent.
func (r *run) checkText(f []observation) string {
	texts := slices.Clone(r.answers)
	for _, o := range f {
		for _, s := range o.sections {
			if s.tag != "final_page_elements" {
				texts = append(texts, s.text)
			}
		}
	}
	return checkNorm(strings.Join(texts, "\n"))
}

// mixedToken is a word of letters and digits, as the ID n12 or a UUID,
// which a record is given anew each run.
var mixedToken = regexp.MustCompile(`(?i)\b(?:[a-z][a-z0-9_-]*[0-9]|[0-9][0-9_-]*[a-z])[a-z0-9_-]*\b`)

// checkNorm normalizes a text for the checks: a word of letters and digits
// reads as #, so that a quote of the ID of the record the recorded run
// made holds on a replay that made another. A number alone is kept, as
// what a count says.
func checkNorm(s string) string {
	return mixedToken.ReplaceAllString(normalize(s), "#")
}

// mintedRun is what changes from run to run in the text of an error: a
// UUID, a run of hex with a digit, or of digits.
var mintedRun = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b|\b[0-9a-f]*\d[0-9a-f]*\b|\d+`)

// errorKinds are the errors of the run, sorted: what the contracts of the
// APIs do not allow, and the errors of the browser.
func (r *run) errorKinds() []string {
	var out []string
	if r.web != nil {
		out = r.web.errorKinds()
	}
	for _, b := range r.breaches {
		call := b.operation
		if call == "" {
			call = b.call
		}
		// What a validator says can quote the value of a field, and so a
		// secret, which the recording is not to keep.
		k := mintedRun.ReplaceAllString(r.secrets.mask(fmt.Sprintf("contract: %s %s %s %s", call, b.In, b.Field, b.Reason)), "#")
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// errorKinds are the console errors and the failed requests of the page,
// masked, with the routes of their URLs, and what changes from run to run
// in their texts as #, sorted. A URL is masked again after its route is
// decoded, as a secret can be in it percent-encoded.
func (d *browserDriver) errorKinds() []string {
	out := []string{}
	add := func(s string) {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, e := range d.page.ConsoleErrors() {
		add(e.Type + ": " + mintedRun.ReplaceAllString(d.secrets.mask(strings.Join(strings.Fields(d.relativeIn(e.Text)), " ")), "#"))
	}
	for _, e := range d.page.NetworkErrors() {
		what := e.Error
		if e.Status > 0 {
			what = strconv.FormatInt(e.Status, 10)
		}
		add(e.Method + " " + d.secrets.mask(routeText(d.relative(d.secrets.mask(e.URL)))) + " " + what)
	}
	slices.Sort(out)
	return out
}

// relativeIn writes the origin of the start URL out of s, so that the text
// of an error on one host is that of the same error on another.
func (d *browserDriver) relativeIn(s string) string {
	u, err := url.Parse(d.cfg.StartURL)
	if err != nil || u.Host == "" {
		return s
	}
	return strings.ReplaceAll(s, u.Scheme+"://"+u.Host, "")
}

// sameRecording says whether two recordings would replay alike, so that a
// recording is not written again for nothing.
func sameRecording(a, b *Recording) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ja) == string(jb)
}

// maxHandoffAnswer bounds each answer to a replayed call the tester is
// told of.
const maxHandoffAnswer = 2000

// handoff tells the tester what was replayed, with what the calls were
// answered, and where the recording stopped, for it to carry on from
// there.
func (r *run) handoff(rp Replay, rec *Recording) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nThe first %d steps of the task were replayed from a recording of an earlier run:\n", rp.Replayed)
	for i, s := range rec.Steps[:rp.Replayed] {
		fmt.Fprintf(&b, "%d. %s%s\n", i+1, s.Tool, describeRecorded(s))
	}
	if len(r.answers) > 0 && slices.ContainsFunc(r.answers, func(a string) bool { return a != "" }) {
		b.WriteString("The calls replayed were answered:\n")
		for i, a := range r.answers {
			if a != "" {
				fmt.Fprintf(&b, "<answer step=\"%d\">\n%s\n</answer>\n", i+1, truncate(a, maxHandoffAnswer))
			}
		}
	}
	s := rec.Steps[rp.DivergedAt-1]
	fmt.Fprintf(&b, "The recording's next step, %s%s, could not be replayed: %s.", s.Tool, describeRecorded(s), rp.Reason)
	if rp.Stale {
		b.WriteString(" The application may have changed.")
	}
	if r.web != nil {
		fmt.Fprintf(&b, " Carry on with the task from the page the browser is on, %s; read it first.", r.secrets.mask(r.web.url))
	} else {
		b.WriteString(" Carry on with the task from there.")
	}
	return b.String()
}

// maxDescribed bounds the input of a call a step is described with.
const maxDescribed = 300

func describeRecorded(s Recorded) string {
	var parts []string
	if s.Target != "" {
		parts = append(parts, "of "+s.Target)
		if len(s.Input) > 0 {
			in, _ := json.Marshal(s.Input)
			parts = append(parts, truncate(string(in), maxDescribed))
		}
		return " " + strings.Join(parts, " ")
	}
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
