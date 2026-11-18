package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// actionTimeout bounds one call of probe-browser, such as a click,
	// when the context of the call allows it longer.
	actionTimeout = 30 * time.Second
	// startTimeout bounds the call that starts the browser in place of
	// actionTimeout, as probe-browser waits for Chrome to start within the
	// timeout of the call: a few seconds as a rule, but more than
	// actionTimeout on a machine as busy as a CI runner with an Android
	// emulator.
	startTimeout = 2 * time.Minute
	// settlePause gives the page's scripts a moment to act on an event,
	// such as to show an error under a field.
	settlePause = 300 * time.Millisecond
	// screenshotQuality is the JPEG quality of the screenshots, which keep
	// text readable at a fraction of the size of PNG.
	screenshotQuality = 80
	// zoomWidth is the width a zoomed region is enlarged to.
	zoomWidth = 1280
	// maxScale is how much probe-browser enlarges a region at most.
	maxScale = 4
	// maxErrors bounds the console and network errors kept, for a page
	// that writes one every frame.
	maxErrors = 200
	// locateTimeout bounds waiting for an element a locator describes.
	locateTimeout = 5 * time.Second
	// maxSlowest is how many of the slowest requests are kept: as many as
	// probe-browser gives of each call, so that those of all the calls,
	// the slowest of each kept, are the slowest of the whole.
	maxSlowest = 5
)

// Call runs a step of probe-browser with with, and returns its res.
type Call func(ctx context.Context, with map[string]any) (map[string]any, error)

// Options are how the browser starts.
type Options struct {
	Headless bool
	// Width and Height are of the viewport, in CSS pixels, which are the
	// pixels of the screenshots and of the points the model clicks.
	Width, Height int
	// StorageState names a file of cookies and localStorage the browser
	// starts with, as of a login another step saved.
	StorageState string
}

// Browser is the tab of a browser probe-browser keeps for the test: each
// member of Page is a step of it, with session, so that the steps drive
// one page, and with one_tab, so that a link that would open a window
// opens its page in the tab the agent drives.
type Browser struct {
	call Call
	o    Options
	// evidence is where probe-browser saves the page at a failed step,
	// which the agent does not keep: a failure is the model's to read.
	evidence string

	mu sync.Mutex
	// started is whether a call has started the browser.
	started bool
	console []ConsoleError
	network []NetworkError
	count   int
	slowest []Request
}

// NewBrowser returns the tab call drives. The browser starts with the
// first call.
func NewBrowser(call Call, o Options) (*Browser, error) {
	dir, err := os.MkdirTemp("", "probe-agent-browser-*")
	if err != nil {
		return nil, err
	}
	return &Browser{call: call, o: o, evidence: dir}, nil
}

// Close removes what the browser left of the failed steps. Probe stops the
// browser itself once the run is over.
func (b *Browser) Close() {
	_ = os.RemoveAll(b.evidence)
}

// action is an action of probe-browser.
type action map[string]any

// pageAtFailure is what probe-browser adds to the error of a failed step,
// where it saved the page, which the agent does not keep.
var pageAtFailure = regexp.MustCompile(` \((?:page at failure: [^)]*|the page could not be (?:captured|saved): [^)]*)\)$`)

// run runs the actions in one step, within actionTimeout or the time ctx
// has left, and keeps the console errors and the requests of the page it
// reports. It returns the results of the actions, and the paths of the
// files they wrote.
func (b *Browser) run(ctx context.Context, timeout time.Duration, actions ...action) (map[string]any, map[string]any, error) {
	b.mu.Lock()
	limit := actionTimeout
	if !b.started {
		limit = startTimeout
	}
	b.mu.Unlock()
	if timeout <= 0 || timeout > limit {
		timeout = limit
	}
	if d, ok := ctx.Deadline(); ok && time.Until(d) < timeout {
		timeout = max(time.Until(d), time.Second)
	}
	list := make([]any, len(actions))
	for i, a := range actions {
		list[i] = map[string]any(a)
	}
	with := map[string]any{
		"session":      true,
		"one_tab":      true,
		"headless":     b.o.Headless,
		"window_w":     b.o.Width,
		"window_h":     b.o.Height,
		"viewport_w":   b.o.Width,
		"viewport_h":   b.o.Height,
		"evidence_dir": b.evidence,
		"timeout":      timeout.String(),
		"actions":      list,
	}
	if b.o.StorageState != "" {
		with["storage_state"] = b.o.StorageState
	}
	res, err := b.call(ctx, with)
	b.record(res)
	if err == nil {
		b.mu.Lock()
		b.started = true
		b.mu.Unlock()
	}
	if err != nil {
		return nil, nil, withoutPageAtFailure(err)
	}
	results, _ := res["results"].(map[string]any)
	files, _ := res["filepaths"].(map[string]any)
	return results, files, nil
}

// text returns the result of id as a string.
func text(results map[string]any, id string) string {
	s, _ := results[id].(string)
	return s
}

// record keeps the console errors and the requests res reports.
func (b *Browser) record(res map[string]any) {
	if res == nil {
		return
	}
	var got struct {
		Console  []ConsoleError `json:"console_errors"`
		Network  []NetworkError `json:"network_errors"`
		Requests Requests       `json:"requests"`
	}
	raw, err := json.Marshal(res)
	if err != nil || json.Unmarshal(raw, &got) != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.console = append(b.console, got.Console[:min(len(got.Console), maxErrors-len(b.console))]...)
	b.network = append(b.network, got.Network[:min(len(got.Network), maxErrors-len(b.network))]...)
	b.count += got.Requests.Count
	for _, r := range got.Requests.Slowest {
		b.slowest = slowest(b.slowest, r, maxSlowest)
	}
}

// settle gives the page a moment for its scripts to act, as on a click.
func settle(ctx context.Context) {
	select {
	case <-time.After(settlePause):
	case <-ctx.Done():
	}
}

// Navigate opens url, or goes back, forward or reloads.
func (b *Browser) Navigate(ctx context.Context, url string) error {
	a := action{"name": "navigate", "url": url}
	switch url {
	case "back", "forward", "reload":
		a = action{"name": url}
	}
	if _, _, err := b.run(ctx, 0, a); err != nil {
		return err
	}
	settle(ctx)
	return nil
}

// Screenshot captures the viewport as JPEG.
func (b *Browser) Screenshot(ctx context.Context) ([]byte, error) {
	return b.capture(ctx, action{"name": "capture_screenshot", "id": "shot", "quality": screenshotQuality})
}

// Zoom captures the region x0, y0 to x1, y1 of the viewport, enlarged to
// zoomWidth, 4 times at most.
func (b *Browser) Zoom(ctx context.Context, x0, y0, x1, y1 float64) ([]byte, error) {
	w, h := x1-x0, y1-y0
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("the region [%g, %g, %g, %g] is empty", x0, y0, x1, y1)
	}
	scale := math.Min(maxScale, math.Max(1, zoomWidth/w))
	return b.capture(ctx, action{"name": "capture_screenshot", "id": "shot", "quality": screenshotQuality,
		"x": x0, "y": y0, "width": w, "height": h, "scale": scale})
}

// capture runs a screenshot, and returns the image, removing its file.
func (b *Browser) capture(ctx context.Context, a action) ([]byte, error) {
	_, files, err := b.run(ctx, 0, a)
	if err != nil {
		return nil, err
	}
	path, _ := files["shot"].(string)
	if path == "" {
		return nil, errors.New("probe-browser saved no screenshot")
	}
	defer os.Remove(path)
	return os.ReadFile(path)
}

// at puts t in a: the ref of its element, or its point.
func at(a action, t Target) action {
	if t.Ref != "" {
		a["ref"] = t.Ref
	} else {
		a["x"], a["y"] = t.X, t.Y
	}
	return a
}

// Click clicks t count times with button, holding the modifiers.
func (b *Browser) Click(ctx context.Context, t Target, button string, count int, modifiers []string) error {
	a := at(action{"name": "click", "button": button}, t)
	if count > 0 {
		a["count"] = count
	}
	if len(modifiers) > 0 {
		mods := make([]any, len(modifiers))
		for i, m := range modifiers {
			mods[i] = m
		}
		a["modifiers"] = mods
	}
	if button == "" {
		delete(a, "button")
	}
	if _, _, err := b.run(ctx, 0, a); err != nil {
		return err
	}
	settle(ctx)
	return nil
}

// Hover moves the mouse onto t.
func (b *Browser) Hover(ctx context.Context, t Target) error {
	if _, _, err := b.run(ctx, 0, at(action{"name": "hover"}, t)); err != nil {
		return err
	}
	settle(ctx)
	return nil
}

// Scroll turns the wheel at x, y, amount notches toward direction.
func (b *Browser) Scroll(ctx context.Context, x, y float64, direction string, amount int) error {
	if amount < 1 {
		amount = 3
	}
	if _, _, err := b.run(ctx, 0, action{"name": "wheel", "x": x, "y": y, "direction": direction, "amount": amount}); err != nil {
		return err
	}
	settle(ctx)
	return nil
}

// ScrollTo scrolls the element of ref into view.
func (b *Browser) ScrollTo(ctx context.Context, ref string) error {
	_, _, err := b.run(ctx, 0, action{"name": "scroll", "ref": ref})
	return err
}

// Type types text into the element that has the focus.
func (b *Browser) Type(ctx context.Context, text string) error {
	if _, _, err := b.run(ctx, 0, action{"name": "type", "value": text}); err != nil {
		return err
	}
	settle(ctx)
	return nil
}

// Key presses the keys repeat times.
func (b *Browser) Key(ctx context.Context, keys string, repeat int) error {
	value, err := keyNames(keys)
	if err != nil {
		return err
	}
	if repeat < 1 {
		repeat = 1
	}
	if _, _, err := b.run(ctx, 0, action{"name": "press", "value": value, "repeat": repeat}); err != nil {
		return err
	}
	settle(ctx)
	return nil
}

// Read returns the elements of the page with their refs.
func (b *Browser) Read(ctx context.Context, filter string, depth int, ref string) (string, error) {
	a := action{"name": "read_page", "id": "read"}
	if filter != "" {
		a["filter"] = filter
	}
	if depth > 0 {
		a["depth"] = depth
	}
	if ref != "" {
		a["ref"] = ref
	}
	results, _, err := b.run(ctx, 0, a)
	return text(results, "read"), err
}

// Text returns the text the page shows.
func (b *Browser) Text(ctx context.Context) (string, error) {
	results, _, err := b.run(ctx, 0, action{"name": "page_text", "id": "text"})
	return text(results, "text"), err
}

// FormInput sets the value of the form field of ref, and says what it did.
func (b *Browser) FormInput(ctx context.Context, ref string, value any) (string, error) {
	results, _, err := b.run(ctx, 0, action{"name": "form_input", "id": "said", "ref": ref, "value": fmt.Sprint(value)})
	if err != nil {
		return "", err
	}
	settle(ctx)
	return text(results, "said"), nil
}

// State returns the URL and the title of the page.
func (b *Browser) State(ctx context.Context) (string, string, error) {
	results, _, err := b.run(ctx, 0, action{"name": "url", "id": "url"}, action{"name": "title", "id": "title"})
	return text(results, "url"), text(results, "title"), err
}

// CatchUp has probe-browser report what the page did since the last
// call, as while the judge decided, so that the console, the requests and
// their errors are whole. A page that is gone has nothing to add.
func (b *Browser) CatchUp(ctx context.Context) {
	// The call, the page saved at a failure included, not the action
	// alone.
	ctx, cancel := context.WithTimeout(ctx, CatchUpTimeout)
	defer cancel()
	_, _, _ = b.run(ctx, CatchUpTimeout, action{"name": "url"})
}

// CatchUpTimeout bounds CatchUp.
const CatchUpTimeout = 5 * time.Second

// Locator describes the element of t so that a later page can find it
// again, or is nil for a point with nothing to act on.
func (b *Browser) Locator(ctx context.Context, t Target) (*Locator, error) {
	results, _, err := b.run(ctx, 0, at(action{"name": "locator", "id": "locator"}, t))
	if err != nil {
		return nil, err
	}
	s := text(results, "locator")
	if s == "" {
		return nil, nil
	}
	var l Locator
	if err := json.Unmarshal([]byte(s), &l); err != nil {
		return nil, fmt.Errorf("reading the locator %q: %w", s, err)
	}
	return &l, nil
}

// manyOf is the error of a locate that found another number of elements
// than its locator counted.
var manyOf = regexp.MustCompile(`the page has (\d+) of them, where it had \d+`)

// Locate returns the ref of the element l describes, waiting up to
// locateTimeout for it to appear, as on a page still being drawn.
func (b *Browser) Locate(ctx context.Context, l Locator) (string, error) {
	loc := map[string]any{"role": l.Role, "name": l.Name}
	if l.Of > 1 {
		loc["nth"], loc["of"] = l.Nth, l.Of
	}
	results, _, err := b.run(ctx, locateTimeout, action{"name": "locate", "id": "ref", "locator": loc})
	switch {
	case err == nil:
		return text(results, "ref"), nil
	case manyOf.MatchString(err.Error()):
		m := manyOf.FindStringSubmatch(err.Error())
		return "", locateError{ErrAmbiguous, fmt.Sprintf("the page has %s of %s, where it had %d", m[1], l, max(l.Of, 1))}
	case strings.Contains(err.Error(), "the page has none"):
		return "", locateError{ErrNotFound, fmt.Sprintf("the page has no %s", l)}
	}
	return "", err
}

// locateError is an error of Locate, which says what the page had, and is
// one of its kinds.
type locateError struct {
	kind error
	msg  string
}

func (e locateError) Error() string { return e.msg }
func (e locateError) Unwrap() error { return e.kind }

// Alerts returns the texts of the alerts the page shows.
func (b *Browser) Alerts(ctx context.Context) ([]string, error) {
	results, _, err := b.run(ctx, 0, action{"name": "alerts", "id": "alerts"})
	if err != nil {
		return nil, err
	}
	s := text(results, "alerts")
	if s == "" {
		return nil, nil
	}
	return strings.Split(s, "\n"), nil
}

// ConsoleErrors returns the errors the page wrote to its console so far.
func (b *Browser) ConsoleErrors() []ConsoleError {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]ConsoleError{}, b.console...)
}

// NetworkErrors returns the requests of the page that failed so far.
func (b *Browser) NetworkErrors() []NetworkError {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]NetworkError{}, b.network...)
}

// Requests returns how many requests the page sent so far, and the slowest
// of them.
func (b *Browser) Requests() Requests {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Requests{Count: b.count, Slowest: append([]Request{}, b.slowest...)}
}

// slowest adds req to list, the slowest first, keeping at most n.
func slowest(list []Request, req Request, n int) []Request {
	i := sort.Search(len(list), func(i int) bool { return list[i].DurationMS < req.DurationMS })
	if i >= n {
		return list
	}
	list = slices.Insert(list, i, req)
	return list[:min(len(list), n)]
}

// shortened is an error of probe-browser told without the page at the
// failure, which the tester reads the page for itself, and is still the
// error it was, as a refusal of the guard.
type shortened struct {
	msg string
	err error
}

func (e *shortened) Error() string { return e.msg }
func (e *shortened) Unwrap() error { return e.err }

// withoutPageAtFailure returns err without the page at the failure.
func withoutPageAtFailure(err error) error {
	msg := pageAtFailure.ReplaceAllString(err.Error(), "")
	if msg == err.Error() {
		return err
	}
	return &shortened{msg: msg, err: err}
}
