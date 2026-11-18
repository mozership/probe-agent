package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mozership/probe-agent/internal/llm"
	"github.com/mozership/probe-agent/internal/page"
)

// Disabled are the members of the browser toolset the agent does not
// offer: those that act on other tabs, as it drives one, those that hold a
// button or a key down, and those the toolset leaves off unless asked.
var Disabled = []string{
	"new_tab", "list_tabs", "switch_tab", "close_tab",
	"left_click_drag", "left_mouse_down", "left_mouse_up", "mouse_move", "middle_click", "hold_key",
	"javascript_exec", "file_upload", "read_console", "read_network",
}

// browserDriver is the browser as a target: the one tab the tester drives
// through the browser toolset.
type browserDriver struct {
	*ledger
	page  page.Page
	hosts hosts

	// shot is the URL of the page the tester last took a screenshot of.
	shot string
	// landing is where the page the test opened first landed.
	landing string
	// url and title are of the page as the model was last told it.
	url, title string
	// recorded are the steps that changed the page, for the recording.
	recorded []Recorded
}

// newBrowserDriver returns the browser of the test l keeps, which drives
// p. The tester may open the pages of the hosts of the test, or of the
// host of its start URL when it names none.
func newBrowserDriver(l *ledger, p page.Page) *browserDriver {
	d := &browserDriver{ledger: l, page: p, hosts: hosts(l.cfg.Hosts)}
	if len(d.hosts) == 0 {
		d.hosts = hosts{hostOf(l.cfg.StartURL)}
	}
	return d
}

var _ Driver = (*browserDriver)(nil)

// Kind is browser.
func (d *browserDriver) Kind() string { return "browser" }

// Open opens the start URL of the test.
func (d *browserDriver) Open(ctx context.Context) error {
	if err := d.page.Navigate(ctx, d.cfg.StartURL); err != nil {
		return fmt.Errorf("opening %s: %w", d.cfg.StartURL, err)
	}
	d.url, d.title, _ = d.page.State(ctx)
	d.landing = d.relative(d.url)
	return nil
}

// Offer offers the browser toolset, but for the members in Disabled.
func (d *browserDriver) Offer(opts *llm.ChatOptions) {
	opts.Browser = &llm.BrowserToolset{Disabled: Disabled}
}

// Brief tells the tester of the browser among other targets.
func (d *browserDriver) Brief() string {
	return `The browser, through the browser toolset:
- It has a single tab. A link that would open another window opens in it.
- Open only pages on these hosts: ` + strings.Join(d.hosts, ", ") + `.
- Do not guess URLs to skip a step unless the task tells you to.
- Read the page before you act on it, and again after an action changed it: refs read before a change may be stale.`
}

// Opening says which page the browser opened.
func (d *browserDriver) Opening() string {
	return "The browser has opened " + d.cfg.StartURL + "."
}

// Handles says whether call is of a member of the browser toolset.
func (d *browserDriver) Handles(call llm.Call) bool {
	return call.Toolset == llm.ToolsetBrowser
}

// tabID is the ID of the one tab, as the model is told it.
const tabID = "tab-1"

// maxWait bounds the wait member, as the toolset does.
const maxWait = 30 * time.Second

// maxText bounds the text of the page a result carries.
const maxText = 50000

// target is a Target of the browser toolset: a ref, or a point of the
// viewport.
type target struct {
	Type string  `json:"type"`
	Ref  string  `json:"ref"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}

func (t target) page() (page.Target, error) {
	switch t.Type {
	case "ref":
		if t.Ref == "" {
			return page.Target{}, errors.New("a ref target needs a ref")
		}
		return page.Target{Ref: t.Ref}, nil
	case "coordinate", "":
		return page.Target{X: t.X, Y: t.Y}, nil
	}
	return page.Target{}, fmt.Errorf("unknown target type %q", t.Type)
}

// browserInput is the input of any member of the browser toolset; each
// member reads the fields it takes.
type browserInput struct {
	URL             string          `json:"url"`
	Target          target          `json:"target"`
	Modifiers       json.RawMessage `json:"modifiers"`
	ScrollDirection string          `json:"scroll_direction"`
	ScrollAmount    int             `json:"scroll_amount"`
	Text            string          `json:"text"`
	Repeat          int             `json:"repeat"`
	Duration        float64         `json:"duration"`
	Filter          string          `json:"filter"`
	Depth           int             `json:"depth"`
	Ref             string          `json:"ref"`
	Query           string          `json:"query"`
	Value           any             `json:"value"`
	Region          []float64       `json:"region"`
}

// modifiers reads the modifiers of a click, given as "ctrl+shift" or as a
// list.
func (in browserInput) modifiers() ([]string, error) {
	if len(in.Modifiers) == 0 || string(in.Modifiers) == "null" {
		return nil, nil
	}
	var s string
	if json.Unmarshal(in.Modifiers, &s) == nil {
		if s == "" {
			return nil, nil
		}
		return strings.Split(s, "+"), nil
	}
	var l []string
	if err := json.Unmarshal(in.Modifiers, &l); err != nil {
		return nil, fmt.Errorf("modifiers: %w", err)
	}
	return l, nil
}

// actions are the members that change the page, after which a screenshot
// is kept and the host of the page is checked.
var actions = map[string]bool{
	"navigate": true, "left_click": true, "right_click": true, "double_click": true, "triple_click": true,
	"hover": true, "scroll": true, "scroll_to": true, "type": true, "key": true, "form_input": true, "wait": true,
}

// Do runs a call of a member of the browser toolset, and returns its
// result and the step that records it.
func (d *browserDriver) Do(ctx context.Context, call llm.Call) (llm.Result, Step) {
	step := Step{Tool: call.Name, Target: d.Kind()}
	// The tester had a screenshot of the page the step acts on.
	pixels := d.shot != "" && d.shot == d.url
	var raw map[string]any
	if json.Unmarshal(call.Input, &raw) == nil && len(raw) > 0 {
		step.Input, _ = d.secrets.maskAny(raw).(map[string]any)
	}
	res := llm.Result{CallID: call.ID}

	var in browserInput
	text, images, err := "", []llm.Image(nil), json.Unmarshal(call.Input, &in)
	// The element a step acts on is described before the step, which can
	// take it off the page, for the recording.
	var loc *page.Locator
	if err == nil && d.cfg.Record && targeted[call.Name] {
		if t, terr := in.Target.page(); terr == nil {
			loc, _ = d.page.Locator(ctx, t)
		}
	}
	if err == nil {
		text, images, err = d.member(ctx, call.Name, in)
	}
	if err != nil {
		// What the model reads is masked, since an error of the page can
		// quote what was typed.
		res.Text = "Error: " + d.secrets.mask(err.Error())
		res.IsError = true
		step.Error = res.Text
	} else {
		res.Text = d.secrets.mask(text)
		res.Images = images
		step.Result = summary(res.Text)
	}

	if actions[call.Name] {
		if msg := d.keepToHosts(ctx); msg != "" {
			res.Text = strings.TrimSpace(res.Text + "\n" + msg)
			res.IsError = true
			step.Error = msg
		}
		if img, err := d.page.Screenshot(ctx); err == nil {
			step.Screenshot = d.save(fmt.Sprintf("step-%02d.jpg", len(d.steps)+1), img)
		}
	}
	if url, title, err := d.page.State(ctx); err == nil {
		step.URL = url
		if !res.IsError && (url != d.url || title != d.title) {
			res.Tabs = []llm.Tab{{ID: tabID, Title: d.secrets.mask(title), URL: d.secrets.mask(url), Active: true}}
		}
		d.url, d.title = url, title
	}
	if d.cfg.Record && actions[call.Name] && !res.IsError {
		d.record(ctx, call.Name, step.Input, loc, step.URL, pixels)
	}
	return res, step
}

// targeted are the members that act on the element of a target.
var targeted = map[string]bool{
	"left_click": true, "right_click": true, "double_click": true, "triple_click": true,
	"hover": true, "scroll_to": true, "form_input": true,
}

// member runs the member name with in, and returns the text and the images
// of its result.
func (d *browserDriver) member(ctx context.Context, name string, in browserInput) (string, []llm.Image, error) {
	p := d.page
	switch name {
	case "navigate":
		switch in.URL {
		case "back", "forward", "reload":
		default:
			if !d.hosts.allows(in.URL) {
				return "", nil, fmt.Errorf("%s is not on a host this test may open; the hosts are %s", in.URL, strings.Join(d.hosts, ", "))
			}
		}
		if err := p.Navigate(ctx, in.URL); err != nil {
			return "", nil, err
		}
		return "Navigated to " + in.URL + ".", nil, nil

	case "screenshot":
		img, err := p.Screenshot(ctx)
		if err != nil {
			return "", nil, err
		}
		d.shot = d.url
		return "", []llm.Image{{MediaType: "image/jpeg", Data: img}}, nil

	case "zoom":
		if len(in.Region) != 4 {
			return "", nil, errors.New("region must be [x0, y0, x1, y1]")
		}
		img, err := p.Zoom(ctx, in.Region[0], in.Region[1], in.Region[2], in.Region[3])
		if err != nil {
			return "", nil, err
		}
		d.shot = d.url
		return "", []llm.Image{{MediaType: "image/jpeg", Data: img}}, nil

	case "left_click", "right_click", "double_click", "triple_click":
		t, err := in.Target.page()
		if err != nil {
			return "", nil, err
		}
		mods, err := in.modifiers()
		if err != nil {
			return "", nil, err
		}
		button, count := "left", 1
		switch name {
		case "right_click":
			button = "right"
		case "double_click":
			count = 2
		case "triple_click":
			count = 3
		}
		if err := p.Click(ctx, t, button, count, mods); err != nil {
			return "", nil, err
		}
		return "Clicked " + describeTarget(t) + ".", nil, nil

	case "hover":
		t, err := in.Target.page()
		if err != nil {
			return "", nil, err
		}
		if err := p.Hover(ctx, t); err != nil {
			return "", nil, err
		}
		return "Hovered over " + describeTarget(t) + ".", nil, nil

	case "scroll":
		if err := p.Scroll(ctx, in.Target.X, in.Target.Y, in.ScrollDirection, in.ScrollAmount); err != nil {
			return "", nil, err
		}
		return "Scrolled " + in.ScrollDirection + ".", nil, nil

	case "scroll_to":
		if in.Target.Ref == "" {
			return "", nil, errors.New("scroll_to needs a ref target")
		}
		if err := p.ScrollTo(ctx, in.Target.Ref); err != nil {
			return "", nil, err
		}
		return "Scrolled to " + in.Target.Ref + ".", nil, nil

	case "type":
		text, err := d.secrets.fill(in.Text)
		if err != nil {
			return "", nil, err
		}
		if err := d.secretsOnHost(ctx, text != in.Text); err != nil {
			return "", nil, err
		}
		if err := p.Type(ctx, text); err != nil {
			return "", nil, err
		}
		return "Typed " + quote(in.Text) + ".", nil, nil

	case "key":
		if err := p.Key(ctx, in.Text, in.Repeat); err != nil {
			return "", nil, err
		}
		return "Pressed " + in.Text + ".", nil, nil

	case "wait":
		d := min(time.Duration(in.Duration*float64(time.Second)), maxWait)
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return "", nil, ctx.Err()
		}
		return fmt.Sprintf("Waited %gs.", d.Seconds()), nil, nil

	case "read_page":
		text, err := p.Read(ctx, in.Filter, in.Depth, in.Ref)
		if err != nil {
			return "", nil, err
		}
		d.seen(text)
		if text == "" {
			return "The page shows nothing.", nil, nil
		}
		return text, nil, nil

	case "find":
		text, err := p.Read(ctx, "all", 0, "")
		if err != nil {
			return "", nil, err
		}
		found := find(strings.Split(text, "\n"), in.Query)
		if len(found) == 0 {
			return "Nothing on the page matches " + quote(in.Query) + ".", nil, nil
		}
		d.seen(strings.Join(found, "\n"))
		return strings.Join(found, "\n"), nil, nil

	case "get_page_text":
		text, err := p.Text(ctx)
		if err != nil {
			return "", nil, err
		}
		text = truncate(text, maxText)
		d.seen(text)
		return text, nil, nil

	case "form_input":
		if in.Target.Ref == "" {
			return "", nil, errors.New("form_input needs a ref target")
		}
		value := in.Value
		if s, ok := value.(string); ok {
			filled, err := d.secrets.fill(s)
			if err != nil {
				return "", nil, err
			}
			if err := d.secretsOnHost(ctx, filled != s); err != nil {
				return "", nil, err
			}
			value = filled
		}
		said, err := p.FormInput(ctx, in.Target.Ref, value)
		return said, nil, err
	}
	return "", nil, fmt.Errorf("%s is not enabled in this environment", name)
}

// secretsOnHost fails, when secret, for a page on a host the test may not
// open, as one a window the page opened between the calls took the tab
// to, and goes back from it, so that a secret is typed on the hosts of
// the test alone.
func (d *browserDriver) secretsOnHost(ctx context.Context, secret bool) error {
	if !secret {
		return nil
	}
	if msg := d.keepToHosts(ctx); msg != "" {
		return errors.New(msg + " The secret was not typed.")
	}
	return nil
}

// keepToHosts goes back from a page on a host the test may not open, and
// says so; it returns "" when the page is on one it may.
func (d *browserDriver) keepToHosts(ctx context.Context) string {
	url, _, err := d.page.State(ctx)
	if err != nil || d.hosts.allows(url) {
		return ""
	}
	msg := fmt.Sprintf("The page went to %s, which is not on a host this test may open, so the browser went back.", url)
	if err := d.page.Navigate(ctx, "back"); err != nil {
		msg += " Going back failed: " + err.Error()
	}
	return msg
}

func describeTarget(t page.Target) string {
	if t.Ref != "" {
		return t.Ref
	}
	return fmt.Sprintf("(%g, %g)", t.X, t.Y)
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// summary is the start of a result, as a step records it.
func summary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return truncate(s, 200)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Final is the page the tester left the browser on.
func (d *browserDriver) Final(ctx context.Context, vision bool) observation {
	url, title, _ := d.page.State(ctx)
	url, title = d.secrets.mask(url), d.secrets.mask(title)
	o := observation{
		heading:   fmt.Sprintf("The page the tester left the browser on, %s (%q):", url, title),
		mediaType: "image/jpeg",
		imageNote: "A screenshot of the final page follows.",
		state:     TargetState{Kind: d.Kind(), URL: url, Title: title},
	}
	var text, elements string
	if t, err := d.page.Text(ctx); err == nil {
		text = truncate(d.secrets.mask(t), 20000)
	}
	if els, err := d.page.Read(ctx, "all", 0, ""); err == nil {
		elements = truncate(d.secrets.mask(els), 20000)
	}
	o.sections = []section{{"final_page_text", text}, {"final_page_elements", elements}}
	if img, err := d.page.Screenshot(ctx); err == nil {
		// The judge is shown the screenshot when the model sees images; it
		// is kept as evidence either way.
		if vision {
			o.image = img
		}
		o.screenshot = d.save("final.jpg", img)
	}
	return o
}
