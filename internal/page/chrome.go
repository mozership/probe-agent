package page

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

//go:embed agent.js
var agentJS string

const (
	// actionTimeout bounds one action, such as a click, when the context
	// of the call allows it longer.
	actionTimeout = 30 * time.Second
	// settleTimeout bounds waiting for the page to load after an action.
	settleTimeout = 5 * time.Second
	// settlePause gives the page's scripts a moment to act on an event,
	// such as to show an error under a field.
	settlePause = 300 * time.Millisecond
	// screenshotQuality is the JPEG quality of the screenshots, which keep
	// text readable at a fraction of the size of PNG.
	screenshotQuality = 80
	// wheelNotch is how far one notch of the wheel scrolls, in pixels.
	wheelNotch = 100
	// zoomWidth is the width a zoomed region is enlarged to.
	zoomWidth = 1280
	// maxErrors bounds the console and network errors kept, for a page
	// that writes one every frame.
	maxErrors = 200
	// locateTimeout bounds waiting for an element a locator describes.
	locateTimeout = 5 * time.Second
	// maxSlowest is how many of the slowest requests are kept.
	maxSlowest = 10
)

// Options are how the browser starts.
type Options struct {
	Headless bool
	// Width and Height are of the viewport, in CSS pixels, which are the
	// pixels of the screenshots and of the points the model clicks.
	Width, Height int
}

// Chrome is a tab of a Chrome the agent started.
type Chrome struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	console  []ConsoleError
	network  []NetworkError
	requests map[network.RequestID]request
	count    int
	slowest  []Request
}

// request is what the events of a request said so far. The events come
// on streams of their own, in no order between them, so a request is kept
// until its events are all in, and only then recorded.
type request struct {
	method, url string
	// sent is when it was sent, and is 0 until it is known.
	sent float64
	// ignored is a request not over HTTP or HTTPS, as of a data URL.
	ignored bool
	// status is of the response, and is 0 until one came.
	status int64
	// finished is when its last byte came, and is 0 until it did.
	finished float64
	failed   bool
	canceled bool
	errText  string
}

// Start starts Chrome with one tab, and records the console errors and
// failed requests of its pages from then on.
func Start(o Options) (*Chrome, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.DisableGPU,
		// Containers usually need it, as for the browser action.
		chromedp.NoSandbox,
		chromedp.WindowSize(o.Width, o.Height),
		chromedp.Flag("headless", o.Headless),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	c := &Chrome{
		ctx:      ctx,
		cancel:   func() { cancel(); allocCancel() },
		requests: map[network.RequestID]request{},
	}
	// The browser lives as long as the context of the first call, which
	// therefore has no deadline.
	if err := chromedp.Do(ctx); err != nil {
		c.cancel()
		return nil, fmt.Errorf("starting Chrome: %w", err)
	}
	c.record()
	err := chromedp.Do(ctx,
		chromedp.EmulateViewport(int64(o.Width), int64(o.Height)),
		chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
			if _, err := chromedp.Call(ctx, network.Enable, network.EnableParams{}); err != nil {
				return err
			}
			_, err := chromedp.Call(ctx, cdppage.AddScriptToEvaluateOnNewDocument, cdppage.AddScriptToEvaluateOnNewDocumentParams{Source: agentJS})
			return err
		}),
	)
	if err != nil {
		c.cancel()
		return nil, fmt.Errorf("setting up Chrome: %w", err)
	}
	return c, nil
}

// Close stops the browser.
func (c *Chrome) Close() {
	c.cancel()
}

// record keeps the console errors and failed requests of the tab until it
// closes.
func (c *Chrome) record() {
	console := chromedp.Console(c.ctx)
	sent := chromedp.Events(c.ctx, network.RequestWillBeSent)
	received := chromedp.Events(c.ctx, network.ResponseReceived)
	failed := chromedp.Events(c.ctx, network.LoadingFailed)
	finished := chromedp.Events(c.ctx, network.LoadingFinished)

	go func() {
		for m, err := range console {
			if err != nil {
				return
			}
			if e, ok := consoleError(m); ok {
				c.mu.Lock()
				if len(c.console) < maxErrors {
					c.console = append(c.console, e)
				}
				c.mu.Unlock()
			}
		}
	}()
	go func() {
		for ev, err := range sent {
			if err != nil {
				return
			}
			c.update(ev.RequestID, func(r *request) {
				if !strings.HasPrefix(ev.Request.URL, "http:") && !strings.HasPrefix(ev.Request.URL, "https:") {
					r.ignored = true
				}
				// A redirect is sent again under the same ID, with the method
				// it now has, and is timed from the first request.
				if r.sent == 0 {
					r.sent = ev.Timestamp.Float64()
				}
				r.method, r.url = ev.Request.Method, ev.Request.URL
			})
		}
	}()
	go func() {
		for ev, err := range received {
			if err != nil {
				return
			}
			if ev.Response == nil {
				continue
			}
			c.update(ev.RequestID, func(r *request) { r.status = ev.Response.Status })
		}
	}()
	go func() {
		for ev, err := range failed {
			if err != nil {
				return
			}
			c.update(ev.RequestID, func(r *request) {
				r.failed, r.canceled, r.errText = true, ev.Canceled, ev.ErrorText
			})
		}
	}()
	go func() {
		for ev, err := range finished {
			if err != nil {
				return
			}
			c.update(ev.RequestID, func(r *request) { r.finished = ev.Timestamp.Float64() })
		}
	}()
}

// update applies an event to the request of id, and records the request
// once its events are all in: a failed request as a network error, and a
// finished one by how long it took, and as a network error if its status
// is 400 or more.
func (c *Chrome) update(id network.RequestID, apply func(*request)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.requests[id]
	apply(&r)
	c.requests[id] = r

	done := r.sent > 0 && (r.failed || (r.finished > 0 && r.status > 0))
	if r.ignored && (r.failed || r.finished > 0) {
		delete(c.requests, id)
		return
	}
	if !done {
		return
	}
	delete(c.requests, id)
	c.count++
	switch {
	case r.status >= 400:
		c.addNetworkError(NetworkError{Method: r.method, URL: r.url, Status: r.status})
	case r.failed && !r.canceled:
		// A request the page or the browser gave up on, as when the page
		// navigated away, is not a failure of the server.
		c.addNetworkError(NetworkError{Method: r.method, URL: r.url, Error: r.errText})
	}
	if !r.failed {
		c.slowest = slowest(c.slowest, Request{
			Method:     r.method,
			URL:        r.url,
			Status:     r.status,
			DurationMS: int64(math.Round((r.finished - r.sent) * 1000)),
		}, maxSlowest)
	}
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

// addNetworkError keeps e, with c.mu held.
func (c *Chrome) addNetworkError(e NetworkError) {
	if len(c.network) < maxErrors {
		c.network = append(c.network, e)
	}
}

// ConsoleErrors returns the console errors recorded so far.
func (c *Chrome) ConsoleErrors() []ConsoleError {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ConsoleError{}, c.console...)
}

// NetworkErrors returns the failed requests recorded so far.
func (c *Chrome) NetworkErrors() []NetworkError {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]NetworkError{}, c.network...)
}

// Requests returns how many requests were sent so far, and the slowest of
// them.
func (c *Chrome) Requests() Requests {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Requests{Count: c.count, Slowest: append([]Request{}, c.slowest...)}
}

// bound returns the context an action runs in: the tab's, ended by ctx and
// by actionTimeout.
func (c *Chrome) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	tctx, cancel := context.WithTimeout(c.ctx, actionTimeout)
	if d, ok := ctx.Deadline(); ok {
		var cancelDeadline context.CancelFunc
		tctx, cancelDeadline = context.WithDeadline(tctx, d)
		prev := cancel
		cancel = func() { cancelDeadline(); prev() }
	}
	stop := context.AfterFunc(ctx, cancel)
	return tctx, func() { stop(); cancel() }
}

func (c *Chrome) do(ctx context.Context, actions ...chromedp.Action[chromedp.Void]) error {
	tctx, cancel := c.bound(ctx)
	defer cancel()
	return chromedp.Do(tctx, actions...)
}

// call runs fn of window.__probeAgent with args in the page. The script is
// evaluated first, as a document opened before the browser was told to
// evaluate it in every document lacks it; it does nothing the second time.
func call[T any](c *Chrome, ctx context.Context, fn string, args ...any) (T, error) {
	encoded := make([]string, len(args))
	for i, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			var zero T
			return zero, err
		}
		encoded[i] = string(b)
	}
	expr := agentJS + ";\nwindow.__probeAgent." + fn + "(" + strings.Join(encoded, ", ") + ")"
	tctx, cancel := c.bound(ctx)
	defer cancel()
	return chromedp.Run(tctx, chromedp.Evaluate[T](expr))
}

// settle waits for the page to load, as after a click that navigated, and
// a moment more for its scripts.
func (c *Chrome) settle(ctx context.Context) {
	deadline := time.Now().Add(settleTimeout)
	for time.Now().Before(deadline) {
		tctx, cancel := c.bound(ctx)
		state, err := chromedp.Run(tctx, chromedp.Evaluate[string]("document.readyState"))
		cancel()
		if err == nil && state == "complete" {
			break
		}
		if ctx.Err() != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	select {
	case <-time.After(settlePause):
	case <-ctx.Done():
	}
}

// Navigate opens url, or goes back, forward or reloads.
func (c *Chrome) Navigate(ctx context.Context, url string) error {
	var a chromedp.Action[chromedp.Void]
	switch url {
	case "back", "forward":
		return c.history(ctx, url)
	case "reload":
		a = chromedp.Reload()
	default:
		a = chromedp.Navigate(url)
	}
	if err := c.do(ctx, a); err != nil {
		return err
	}
	c.settle(ctx)
	return nil
}

// history goes back or forward in the tab's history. A page restored from
// the browser's cache fires no load, which chromedp's NavigateBack waits
// for, so the page is taken to have moved once its URL changed.
func (c *Chrome) history(ctx context.Context, dir string) error {
	before, _, err := c.State(ctx)
	if err != nil {
		return err
	}
	if err := c.do(ctx, chromedp.Evaluate[chromedp.Void]("history."+dir+"()")); err != nil {
		return err
	}
	deadline := time.Now().Add(settleTimeout)
	for time.Now().Before(deadline) {
		if now, _, err := c.State(ctx); err == nil && now != before {
			c.settle(ctx)
			return nil
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("there is no page to go %s to", dir)
}

// Screenshot captures the viewport.
func (c *Chrome) Screenshot(ctx context.Context) ([]byte, error) {
	return c.capture(ctx, nil)
}

// Zoom captures a region of the viewport, enlarged to zoomWidth.
func (c *Chrome) Zoom(ctx context.Context, x0, y0, x1, y1 float64) ([]byte, error) {
	w, h := x1-x0, y1-y0
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("the region [%g, %g, %g, %g] is empty", x0, y0, x1, y1)
	}
	scale := math.Min(4, math.Max(1, zoomWidth/w))
	// The clip is of the document, so the scroll offset is added to the
	// viewport's point.
	tctx, cancel := c.bound(ctx)
	defer cancel()
	scroll, err := chromedp.Run(tctx, chromedp.Evaluate[[2]float64]("[window.scrollX, window.scrollY]"))
	if err != nil {
		return nil, err
	}
	return c.capture(ctx, &cdppage.Viewport{X: x0 + scroll[0], Y: y0 + scroll[1], Width: w, Height: h, Scale: scale})
}

func (c *Chrome) capture(ctx context.Context, clip *cdppage.Viewport) ([]byte, error) {
	tctx, cancel := c.bound(ctx)
	defer cancel()
	q := int64(screenshotQuality)
	res, err := chromedp.Call(tctx, cdppage.CaptureScreenshot, cdppage.CaptureScreenshotParams{
		Format:  cdppage.CaptureScreenshotFormatJpeg,
		Quality: &q,
		Clip:    clip,
	})
	if err != nil {
		return nil, err
	}
	return res.Data, nil
}

// point returns where a pointer action on t goes, scrolling the element of
// a ref into view.
func (c *Chrome) point(ctx context.Context, t Target) (float64, float64, error) {
	if t.Ref == "" {
		return t.X, t.Y, nil
	}
	p, err := call[struct{ X, Y float64 }](c, ctx, "point", t.Ref)
	if err != nil {
		return 0, 0, err
	}
	return p.X, p.Y, nil
}

// Click clicks t.
func (c *Chrome) Click(ctx context.Context, t Target, button string, count int, modifiers []string) error {
	mods, err := buttonModifiers(modifiers)
	if err != nil {
		return err
	}
	x, y, err := c.point(ctx, t)
	if err != nil {
		return err
	}
	if count < 1 {
		count = 1
	}
	var clicks []chromedp.Action[chromedp.Void]
	// A double click is a click and a click with the count 2, as a mouse
	// sends it.
	for i := 1; i <= count; i++ {
		clicks = append(clicks, chromedp.MouseClickXY(x, y, chromedp.Button(button), chromedp.ClickCount(i), chromedp.ButtonModifiers(mods...)))
	}
	if err := c.do(ctx, clicks...); err != nil {
		return err
	}
	c.settle(ctx)
	return nil
}

// Hover moves the pointer onto t.
func (c *Chrome) Hover(ctx context.Context, t Target) error {
	x, y, err := c.point(ctx, t)
	if err != nil {
		return err
	}
	if err := c.do(ctx, chromedp.MouseEvent(input.DispatchMouseEventTypeMouseMoved, x, y)); err != nil {
		return err
	}
	c.settle(ctx)
	return nil
}

// Scroll turns the wheel at x, y.
func (c *Chrome) Scroll(ctx context.Context, x, y float64, direction string, amount int) error {
	if amount < 1 {
		amount = 3
	}
	d := float64(amount * wheelNotch)
	var dx, dy float64
	switch direction {
	case "up":
		dy = -d
	case "down":
		dy = d
	case "left":
		dx = -d
	case "right":
		dx = d
	default:
		return fmt.Errorf("unknown scroll direction %q", direction)
	}
	wheel := chromedp.MouseEvent(input.DispatchMouseEventTypeMouseWheel, x, y, func(p *input.DispatchMouseEventParams) {
		p.DeltaX, p.DeltaY = dx, dy
	})
	if err := c.do(ctx, wheel); err != nil {
		return err
	}
	c.settle(ctx)
	return nil
}

// ScrollTo scrolls the element of ref into view.
func (c *Chrome) ScrollTo(ctx context.Context, ref string) error {
	_, err := call[chromedp.Void](c, ctx, "scrollTo", ref)
	return err
}

// Type types text into the element that has the focus.
func (c *Chrome) Type(ctx context.Context, text string) error {
	if err := c.do(ctx, chromedp.KeyEvent(text)); err != nil {
		return err
	}
	c.settle(ctx)
	return nil
}

// Key presses keys repeat times.
func (c *Chrome) Key(ctx context.Context, keys string, repeat int) error {
	chords, err := parseKeys(keys)
	if err != nil {
		return err
	}
	if repeat < 1 {
		repeat = 1
	}
	var actions []chromedp.Action[chromedp.Void]
	for range repeat {
		for _, ch := range chords {
			actions = append(actions, chromedp.KeyEvent(ch.key, chromedp.KeyModifiers(ch.modifiers...)))
		}
	}
	if err := c.do(ctx, actions...); err != nil {
		return err
	}
	c.settle(ctx)
	return nil
}

// Read returns the elements of the page with their refs.
func (c *Chrome) Read(ctx context.Context, filter string, depth int, ref string) (string, error) {
	if depth < 1 {
		depth = 15
	}
	return call[string](c, ctx, "read", filter, depth, ref)
}

// Locator describes the element of t so that a later page can find it
// again.
func (c *Chrome) Locator(ctx context.Context, t Target) (*Locator, error) {
	return call[*Locator](c, ctx, "locator", t.Ref, t.X, t.Y)
}

// Locate returns the ref of the element l describes, waiting up to
// locateTimeout for it to appear, as on a page still being drawn.
func (c *Chrome) Locate(ctx context.Context, l Locator) (string, error) {
	deadline := time.Now().Add(locateTimeout)
	for {
		found, err := call[struct {
			Ref   string `json:"ref"`
			Count int    `json:"count"`
		}](c, ctx, "locate", l)
		switch {
		case err == nil && found.Ref != "":
			return found.Ref, nil
		case err == nil && found.Count > 0:
			// Waiting cannot tell apart elements that are all there.
			return "", locateError{ErrAmbiguous, fmt.Sprintf("the page has %d of %s, where it had %d", found.Count, l, max(l.Of, 1))}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if err != nil {
				return "", err
			}
			return "", locateError{ErrNotFound, fmt.Sprintf("the page has no %s", l)}
		}
		time.Sleep(200 * time.Millisecond)
	}
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
func (c *Chrome) Alerts(ctx context.Context) ([]string, error) {
	return call[[]string](c, ctx, "alerts")
}

// Text returns the text the page shows.
func (c *Chrome) Text(ctx context.Context) (string, error) {
	return call[string](c, ctx, "text")
}

// FormInput sets the value of the form field of ref.
func (c *Chrome) FormInput(ctx context.Context, ref string, value any) (string, error) {
	s, err := call[string](c, ctx, "formInput", ref, value)
	if err != nil {
		return "", err
	}
	c.settle(ctx)
	return s, nil
}

// State returns the URL and the title of the page.
func (c *Chrome) State(ctx context.Context) (string, string, error) {
	tctx, cancel := c.bound(ctx)
	defer cancel()
	url, err := chromedp.Run(tctx, chromedp.Location())
	if err != nil {
		return "", "", err
	}
	title, err := chromedp.Run(tctx, chromedp.Title())
	return url, title, err
}

// consoleError returns the error m is, and false for a message that is not
// one, as the browser action reads them.
func consoleError(m chromedp.ConsoleMessage) (ConsoleError, bool) {
	// An entry of the browser log has a source; it is the browser speaking,
	// not the page.
	if m.Source != "" {
		return ConsoleError{}, false
	}
	switch m.Type {
	case chromedp.ConsoleError, "assert", chromedp.ConsoleException:
	default:
		return ConsoleError{}, false
	}
	e := ConsoleError{Type: string(m.Type), Text: m.Text, URL: m.URL}
	if m.URL != "" {
		e.Line, e.Column = m.Line+1, m.Column+1
	}
	return e, true
}
