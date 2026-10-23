package page

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
)

const formPage = `<!doctype html>
<html><head><title>Sign up</title></head><body>
<main>
  <h1>Create your account</h1>
  <form id="f" onsubmit="event.preventDefault(); document.getElementById('out').textContent = 'Hello, ' + document.getElementById('email').value + ' (' + document.getElementById('plan').value + ', ' + document.getElementById('terms').checked + ')';">
    <label for="email">Email</label><input id="email" type="email" required>
    <label for="pw">Password</label><input id="pw" type="password">
    <label for="plan">Plan</label><select id="plan"><option value="free">Free</option><option value="pro">Pro</option></select>
    <label><input id="terms" type="checkbox"> I agree</label>
    <button type="submit">Sign up</button>
  </form>
  <p id="out" role="status"></p>
  <div style="display:none"><button>Hidden</button></div>
  <a href="/next">Next page</a>
  <img src="/missing.png" alt="logo">
</main>
</body></html>`

// startChrome starts Chrome for a test, and skips the test where there is
// none to start.
func startChrome(t *testing.T) *Chrome {
	t.Helper()
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	c, err := Start(Options{Headless: true, Width: 1280, Height: 800})
	if err != nil {
		t.Skipf("no Chrome to start: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, formPage)
	})
	mux.HandleFunc("/timed", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Timed</title><p id="out"></p>
<script>fetch("/slow", {method: "POST"}).then(r => r.text()).then(t => document.getElementById("out").textContent = t)</script>`)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		fmt.Fprint(w, "slow answer")
	})
	mux.HandleFunc("/next", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Next</title><h1>Second page</h1><p role="alert">Saved   3 notes</p><ul><li>A <button>Delete</button></li><li>B <button onclick="this.textContent='Gone'">Delete</button></li></ul>`)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// refOf returns the ref of the first line of read that matches pattern.
func refOf(t *testing.T, read, pattern string) string {
	t.Helper()
	re := regexp.MustCompile(pattern + `.*\[(ref_\d+)\]`)
	for _, line := range strings.Split(read, "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	t.Fatalf("no line matches %q in:\n%s", pattern, read)
	return ""
}

func TestChrome_ReadsAndFillsAForm(t *testing.T) {
	c := startChrome(t)
	s := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := c.Navigate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	read, err := c.Read(ctx, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`heading "Create your account" level=1`,
		`textbox "Email" type=email required`,
		`textbox "Password" type=password`,
		`combobox "Plan" value="Free" options="Free | Pro"`,
		`checkbox "I agree" unchecked`,
		`button "Sign up"`,
		`link "Next page" href="/next"`,
		`img "logo"`,
	} {
		if !strings.Contains(read, want) {
			t.Errorf("the page read lacks %q:\n%s", want, read)
		}
	}
	if strings.Contains(read, "Hidden") {
		t.Errorf("an element that is not displayed was read:\n%s", read)
	}

	// The email is typed as a user types it, the rest set as form fields.
	if err := c.Click(ctx, Target{Ref: refOf(t, read, `textbox "Email"`)}, "left", 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Type(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FormInput(ctx, refOf(t, read, `textbox "Password"`), "s3cret"); err != nil {
		t.Fatal(err)
	}
	if said, err := c.FormInput(ctx, refOf(t, read, `combobox "Plan"`), "Pro"); err != nil || !strings.Contains(said, "Pro") {
		t.Fatalf("FormInput(Plan) = %q, %v", said, err)
	}
	if _, err := c.FormInput(ctx, refOf(t, read, `checkbox "I agree"`), true); err != nil {
		t.Fatal(err)
	}

	read, err = c.Read(ctx, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read, `value="alice@example.com"`) || !strings.Contains(read, `value="••••"`) {
		t.Errorf("the fields are not read as filled, the password hidden:\n%s", read)
	}
	if strings.Contains(read, "s3cret") {
		t.Errorf("the password was read:\n%s", read)
	}

	if err := c.Click(ctx, Target{Ref: refOf(t, read, `button "Sign up"`)}, "left", 1, nil); err != nil {
		t.Fatal(err)
	}
	text, err := c.Text(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Hello, alice@example.com (pro, true)") {
		t.Errorf("the form was not submitted as filled; the page says:\n%s", text)
	}
}

func TestChrome_RefsGoStaleOnNavigation(t *testing.T) {
	c := startChrome(t)
	s := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := c.Navigate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	read, err := c.Read(ctx, "interactive", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	link := refOf(t, read, `link "Next page"`)
	if err := c.Click(ctx, Target{Ref: link}, "left", 1, nil); err != nil {
		t.Fatal(err)
	}
	url, title, err := c.State(ctx)
	if err != nil || title != "Next" || !strings.HasSuffix(url, "/next") {
		t.Fatalf("State() = %q, %q, %v; want the next page", url, title, err)
	}
	err = c.Click(ctx, Target{Ref: link}, "left", 1, nil)
	if err == nil || !strings.Contains(err.Error(), "is not on the page any more") {
		t.Errorf("clicking a ref of the page before = %v; want that it is stale", err)
	}

	if err := c.Navigate(ctx, "back"); err != nil {
		t.Fatal(err)
	}
	if _, title, _ := c.State(ctx); title != "Sign up" {
		t.Errorf("back went to %q", title)
	}
}

func TestChrome_RecordsFailedRequests(t *testing.T) {
	c := startChrome(t)
	s := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := c.Navigate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	// The image of the page is not found.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range c.NetworkErrors() {
			if strings.HasSuffix(e.URL, "/missing.png") && e.Status == 404 && e.Method == "GET" {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("the 404 of the image was not recorded: %+v", c.NetworkErrors())
}

func TestChrome_Screenshot(t *testing.T) {
	c := startChrome(t)
	s := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := c.Navigate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	img, err := c.Screenshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(img) < 3 || img[0] != 0xff || img[1] != 0xd8 {
		t.Errorf("the screenshot is not a JPEG")
	}
	zoomed, err := c.Zoom(ctx, 0, 0, 320, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(zoomed) < 3 || zoomed[0] != 0xff {
		t.Errorf("the zoomed screenshot is not a JPEG")
	}
}

func TestChrome_TimesRequests(t *testing.T) {
	c := startChrome(t)
	s := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := c.Navigate(ctx, s.URL+"/timed"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := c.Requests()
		if len(r.Slowest) > 0 && strings.HasSuffix(r.Slowest[0].URL, "/slow") {
			got := r.Slowest[0]
			if got.Method != "POST" || got.Status != 200 || got.DurationMS < 400 || r.Count < 2 {
				t.Errorf("requests %+v", r)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("the slow request was not timed: %+v", c.Requests())
}

// TestChrome_RecordsRequestsInAnyOrder checks that the events of a request,
// which come on streams of their own, are recorded whatever order they are
// handled in.
func TestChrome_RecordsRequestsInAnyOrder(t *testing.T) {
	sent := func(method, url string, at float64) func(*request) {
		return func(r *request) {
			if r.sent == 0 {
				r.sent = at
			}
			r.method, r.url = method, url
		}
	}
	status := func(s int64) func(*request) { return func(r *request) { r.status = s } }
	finished := func(at float64) func(*request) { return func(r *request) { r.finished = at } }
	failed := func(canceled bool) func(*request) {
		return func(r *request) { r.failed, r.canceled, r.errText = true, canceled, "net::ERR_FAILED" }
	}

	c := &Chrome{requests: map[network.RequestID]request{}}
	// A POST redirected to a GET, its response handled after its end.
	c.update("1", sent("POST", "http://a.test/signup", 10))
	c.update("1", sent("GET", "http://a.test/welcome", 10.1))
	c.update("1", finished(10.5))
	c.update("1", status(200))
	// A 404 whose sending is handled last.
	c.update("2", status(404))
	c.update("2", finished(11.02))
	c.update("2", sent("GET", "http://a.test/favicon.ico", 11))
	// A request that failed, and one the page gave up on.
	c.update("3", failed(false))
	c.update("3", sent("GET", "http://a.test/api", 12))
	c.update("4", sent("GET", "http://a.test/gone", 13))
	c.update("4", failed(true))
	// A request that has not finished.
	c.update("5", sent("GET", "http://a.test/slow", 14))

	r := c.Requests()
	if r.Count != 4 || len(r.Slowest) != 2 {
		t.Fatalf("requests %+v", r)
	}
	if got := r.Slowest[0]; got.Method != "GET" || got.URL != "http://a.test/welcome" || got.Status != 200 || got.DurationMS != 500 {
		t.Errorf("the redirect %+v", got)
	}
	if got := r.Slowest[1]; got.Status != 404 || got.DurationMS != 20 {
		t.Errorf("the 404 %+v", got)
	}
	errs := c.NetworkErrors()
	if len(errs) != 2 || errs[0].URL != "http://a.test/favicon.ico" || errs[0].Status != 404 || errs[1].Error != "net::ERR_FAILED" || errs[1].Method != "GET" {
		t.Errorf("network errors %+v", errs)
	}
	if len(c.requests) != 1 {
		t.Errorf("requests kept: %v", c.requests)
	}
}

func TestSlowest(t *testing.T) {
	var list []Request
	for _, d := range []int64{5, 30, 10, 30, 1, 20} {
		list = slowest(list, Request{DurationMS: d}, 3)
	}
	var got []int64
	for _, r := range list {
		got = append(got, r.DurationMS)
	}
	if fmt.Sprint(got) != "[30 30 20]" {
		t.Errorf("slowest = %v", got)
	}
}

func TestChrome_LocatesElementsAgain(t *testing.T) {
	c := startChrome(t)
	s := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := c.Navigate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	read, err := c.Read(ctx, "interactive", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	l, err := c.Locator(ctx, Target{Ref: refOf(t, read, `textbox "Email"`)})
	if err != nil || l == nil || *l != (Locator{Role: "textbox", Name: "Email"}) {
		t.Fatalf("locator %+v, %v", l, err)
	}
	pt, err := call[map[string]float64](c, ctx, "point", refOf(t, read, `button "Sign up"`))
	if err != nil {
		t.Fatal(err)
	}
	if at, err := c.Locator(ctx, Target{X: pt["x"], Y: pt["y"]}); err != nil || at == nil || at.Role != "button" || at.Name != "Sign up" {
		t.Errorf("locator at a point %+v, %v", at, err)
	}
	if none, err := c.Locator(ctx, Target{X: 1, Y: 790}); err != nil || none != nil {
		t.Errorf("locator at a blank point %+v, %v", none, err)
	}

	// The page loaded again has the field under another ref.
	if err := c.Navigate(ctx, "reload"); err != nil {
		t.Fatal(err)
	}
	ref, err := c.Locate(ctx, *l)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.FormInput(ctx, ref, "carol@example.com"); err != nil {
		t.Errorf("the located field: %v", err)
	}

	// The second of two buttons named alike.
	if err := c.Navigate(ctx, s.URL+"/next"); err != nil {
		t.Fatal(err)
	}
	read, _ = c.Read(ctx, "interactive", 0, "")
	refs := regexp.MustCompile(`button "Delete" \[(ref_\d+)\]`).FindAllStringSubmatch(read, -1)
	if len(refs) != 2 {
		t.Fatalf("read %s", read)
	}
	second, err := c.Locator(ctx, Target{Ref: refs[1][1]})
	if err != nil || second.Nth != 1 || second.Of != 2 {
		t.Fatalf("locator of the second %+v, %v", second, err)
	}
	ref, err = c.Locate(ctx, *second)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Click(ctx, Target{Ref: ref}, "left", 1, nil); err != nil {
		t.Fatal(err)
	}
	if text, _ := c.Text(ctx); !strings.Contains(text, "B Gone") {
		t.Errorf("the click went to another button: %q", text)
	}
	// One "Delete" is left where there were two: which is meant cannot be
	// told, and waiting does not help.
	start := time.Now()
	if _, err := c.Locate(ctx, *second); !errors.Is(err, ErrAmbiguous) || time.Since(start) > 2*time.Second {
		t.Errorf("located among another number: %v after %s", err, time.Since(start))
	}
	if alerts, err := c.Alerts(ctx); err != nil || len(alerts) != 1 || alerts[0] != "Saved 3 notes" {
		t.Errorf("alerts %q, %v", alerts, err)
	}

	short, cancelShort := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancelShort()
	if _, err := c.Locate(short, Locator{Role: "button", Name: "Nowhere"}); err == nil || errors.Is(err, ErrAmbiguous) {
		t.Errorf("located a button the page lacks")
	}
}
