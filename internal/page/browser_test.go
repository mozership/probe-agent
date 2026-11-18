package page

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mozership/probe"
	"github.com/mozership/probe/actionrpc"
	// The tests build probe-browser, whose packages go.sum keeps for it.
	_ "github.com/mozership/probe-browser/browser"
)

const formPage = `<!doctype html>
<html><head><title>Sign up</title><link rel="icon" href="data:,"></head><body>
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
  <a id="help" href="/next" target="_blank">Help in a new window</a>
  <img src="/missing.png" alt="logo">
</main>
<script>console.error("a page error")</script>
</body></html>`

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
		fmt.Fprint(w, `<!doctype html><title>Timed</title><link rel="icon" href="data:,"><p id="out"></p>
<script>fetch("/slow", {method: "POST"}).then(r => r.text()).then(t => document.getElementById("out").textContent = t)</script>`)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		fmt.Fprint(w, "slow answer")
	})
	mux.HandleFunc("/next", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Next</title><link rel="icon" href="data:,"><h1>Second page</h1><p role="alert">Saved   3 notes</p><ul><li>A <button>Delete</button></li><li>B <button onclick="this.textContent='Gone'">Delete</button></li></ul>`)
	})
	mux.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("sid")
		if err != nil {
			fmt.Fprint(w, `<title>nobody</title><link rel="icon" href="data:,">`)
			return
		}
		fmt.Fprintf(w, `<title>%s</title><link rel="icon" href="data:,">`, c.Value)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

var (
	buildOnce   sync.Once
	actionDir   string
	errNoAction error
)

// probeBrowserAction builds probe-browser, of the version go.mod requires,
// into a directory with its action.yml, as a local action, once for the
// tests.
func probeBrowserAction(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "probe-agent-test-browser-*")
		if err != nil {
			errNoAction = err
			return
		}
		src, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/mozership/probe-browser").Output()
		if err != nil {
			errNoAction = fmt.Errorf("finding probe-browser: %w", err)
			return
		}
		build := exec.Command("go", "build", "-o", filepath.Join(dir, "probe-browser"), "github.com/mozership/probe-browser")
		if out, err := build.CombinedOutput(); err != nil {
			errNoAction = fmt.Errorf("building probe-browser: %v: %s", err, out)
			return
		}
		// The e2e's action.yml of probe-browser runs the executable beside
		// it, with the params of the version built.
		manifest, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(src)), "e2e", "action", "action.yml"))
		if err != nil {
			errNoAction = err
			return
		}
		if err := os.WriteFile(filepath.Join(dir, "action.yml"), manifest, 0o644); err != nil {
			errNoAction = err
			return
		}
		actionDir = dir
	})
	if errNoAction != nil {
		t.Fatalf("no probe-browser to run: %v", errNoAction)
	}
	return actionDir
}

// startBrowser returns a tab of probe-browser, called as the agent calls
// it, and skips the test where there is no Chrome to start.
func startBrowser(t *testing.T, o Options) *Browser {
	t.Helper()
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go to build probe-browser with")
	}
	dir := probeBrowserAction(t)
	t.Setenv("PROBE_BROWSER_CACHE_DIR", t.TempDir())
	call := actionrpc.Call{
		Step:      actionrpc.Step{RunID: t.Name() + "-" + fmt.Sprint(time.Now().UnixNano())},
		Cleanups:  &actionrpc.Cleanups{},
		Processes: &actionrpc.Processes{},
	}
	caller := probe.NewActionCaller(call, "", probe.WithCallerLogOutput(nopWriter{}))
	if o.Width == 0 {
		o.Width, o.Height = 1280, 800
	}
	o.Headless = true
	b, err := NewBrowser(func(ctx context.Context, with map[string]any) (map[string]any, error) {
		cr, err := caller.Call(ctx, dir, with)
		if err != nil {
			return nil, err
		}
		res, _ := cr.Result["res"].(map[string]any)
		return res, nil
	}, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		b.Close()
		// Probe stops the browser once the run is over; the test is its run.
		for _, p := range call.Processes.List() {
			_ = syscall.Kill(-p.PID, syscall.SIGKILL)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, _, err := b.State(ctx); err != nil {
		if strings.Contains(err.Error(), "executable file not found") || strings.Contains(err.Error(), "browser did not start") {
			t.Skipf("no Chrome to start: %v", err)
		}
		t.Fatalf("starting the browser: %v", err)
	}
	return b
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// refOf returns the ref of the first line of read that matches pattern.
func refOf(t *testing.T, read, pattern string) string {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(pattern) + `.*\[(ref_[a-z]+\d+)\]`)
	for _, line := range strings.Split(read, "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	t.Fatalf("no line matches %q in:\n%s", pattern, read)
	return ""
}

func TestBrowser_ReadsAndFillsAForm(t *testing.T) {
	b := startBrowser(t, Options{})
	s := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := b.Navigate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	read, err := b.Read(ctx, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`heading "Create your account" level=1`,
		`textbox "Email" type=email required`,
		`combobox "Plan" value="Free" options="Free | Pro"`,
		`checkbox "I agree" unchecked`,
		`button "Sign up"`,
		`link "Next page" href="/next"`,
	} {
		if !strings.Contains(read, want) {
			t.Errorf("the page read lacks %q:\n%s", want, read)
		}
	}
	if strings.Contains(read, "Hidden") {
		t.Errorf("an element that is not displayed was read:\n%s", read)
	}

	// The email is typed as a user types it, the rest set as form fields.
	if err := b.Click(ctx, Target{Ref: refOf(t, read, `textbox "Email"`)}, "left", 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Type(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.FormInput(ctx, refOf(t, read, `textbox "Password"`), "s3cret"); err != nil {
		t.Fatal(err)
	}
	if said, err := b.FormInput(ctx, refOf(t, read, `combobox "Plan"`), "Pro"); err != nil || !strings.Contains(said, "Pro") {
		t.Fatalf("FormInput(Plan) = %q, %v", said, err)
	}
	if _, err := b.FormInput(ctx, refOf(t, read, `checkbox "I agree"`), true); err != nil {
		t.Fatal(err)
	}
	read, err = b.Read(ctx, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read, `value="alice@example.com"`) || !strings.Contains(read, `value="••••"`) || strings.Contains(read, "s3cret") {
		t.Errorf("the fields are not read as filled, the password hidden:\n%s", read)
	}
	if err := b.Click(ctx, Target{Ref: refOf(t, read, `button "Sign up"`)}, "left", 1, nil); err != nil {
		t.Fatal(err)
	}
	text, err := b.Text(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Hello, alice@example.com (pro, true)") {
		t.Errorf("the form was not submitted as filled; the page says:\n%s", text)
	}
}

func TestBrowser_RefsGoStaleOnNavigation(t *testing.T) {
	b := startBrowser(t, Options{})
	s := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := b.Navigate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	read, err := b.Read(ctx, "interactive", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	link := refOf(t, read, `link "Next page"`)
	if err := b.Click(ctx, Target{Ref: link}, "left", 1, nil); err != nil {
		t.Fatal(err)
	}
	url, title, err := b.State(ctx)
	if err != nil || title != "Next" || !strings.HasSuffix(url, "/next") {
		t.Fatalf("State() = %q, %q, %v; want the next page", url, title, err)
	}
	err = b.Click(ctx, Target{Ref: link}, "left", 1, nil)
	if err == nil || !strings.Contains(err.Error(), "is not on the page any more") {
		t.Errorf("clicking a ref of the page before = %v; want that it is stale", err)
	}
	if err != nil && strings.Contains(err.Error(), "page at failure") {
		t.Errorf("the error names where the page was saved: %v", err)
	}
	if err := b.Navigate(ctx, "back"); err != nil {
		t.Fatal(err)
	}
	if _, title, _ := b.State(ctx); title != "Sign up" {
		t.Errorf("back went to %q", title)
	}
}

// TestBrowser_KeepsToOneTab follows a link that opens a new window, which
// opens its page in the tab the agent drives.
func TestBrowser_KeepsToOneTab(t *testing.T) {
	b := startBrowser(t, Options{})
	s := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := b.Navigate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	read, err := b.Read(ctx, "interactive", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Click(ctx, Target{Ref: refOf(t, read, `link "Help in a new window"`)}, "left", 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, title, err := b.State(ctx); err != nil || title != "Next" {
		t.Errorf("the tab shows %q, %v; want the page of the link", title, err)
	}
}

func TestBrowser_ScreenshotsOfTheViewport(t *testing.T) {
	b := startBrowser(t, Options{Width: 1024, Height: 640})
	s := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := b.Navigate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		shot func() ([]byte, error)
		w, h int
	}{
		{"the viewport", func() ([]byte, error) { return b.Screenshot(ctx) }, 1024, 640},
		// 1280 over 320 is enlarged 4 times.
		{"a region", func() ([]byte, error) { return b.Zoom(ctx, 0, 0, 320, 200) }, 1280, 800},
	} {
		img, err := tc.shot()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(img))
		if err != nil || format != "jpeg" || cfg.Width != tc.w || cfg.Height != tc.h {
			t.Errorf("%s: a %s of %dx%d, %v; want a jpeg of %dx%d", tc.name, format, cfg.Width, cfg.Height, err, tc.w, tc.h)
		}
	}
}

func TestBrowser_RecordsTheConsoleAndTheRequests(t *testing.T) {
	b := startBrowser(t, Options{})
	s := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := b.Navigate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	if err := b.Navigate(ctx, s.URL+"/timed"); err != nil {
		t.Fatal(err)
	}
	// The slow answer comes while the agent reads the page.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r := b.Requests(); len(r.Slowest) > 0 && strings.HasSuffix(r.Slowest[0].URL, "/slow") {
			break
		}
		if _, err := b.Text(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if r := b.Requests(); len(r.Slowest) == 0 || !strings.HasSuffix(r.Slowest[0].URL, "/slow") || r.Slowest[0].Method != "POST" || r.Slowest[0].DurationMS < 400 || r.Count < 3 {
		t.Errorf("requests %+v", r)
	}
	var missing bool
	for _, e := range b.NetworkErrors() {
		missing = missing || (strings.HasSuffix(e.URL, "/missing.png") && e.Status == 404 && e.Method == "GET")
	}
	if !missing {
		t.Errorf("the 404 of the image was not recorded: %+v", b.NetworkErrors())
	}
	if c := b.ConsoleErrors(); len(c) != 1 || c[0].Text != "a page error" || c[0].Line == 0 {
		t.Errorf("console errors %+v", c)
	}
}

func TestBrowser_LocatesElementsAgain(t *testing.T) {
	b := startBrowser(t, Options{})
	s := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := b.Navigate(ctx, s.URL+"/next"); err != nil {
		t.Fatal(err)
	}
	read, err := b.Read(ctx, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	second := regexp.MustCompile(`button "Delete" \[(ref_[a-z]+\d+)\]`).FindAllStringSubmatch(read, -1)
	if len(second) != 2 {
		t.Fatalf("two Delete buttons, read:\n%s", read)
	}
	l, err := b.Locator(ctx, Target{Ref: second[1][1]})
	if err != nil || l == nil || *l != (Locator{Role: "button", Name: "Delete", Nth: 1, Of: 2}) {
		t.Fatalf("locator %+v, %v", l, err)
	}
	if err := b.Navigate(ctx, "reload"); err != nil {
		t.Fatal(err)
	}
	again, err := b.Locate(ctx, *l)
	if err != nil || again == "" {
		t.Fatalf("located %q, %v", again, err)
	}
	if err := b.Click(ctx, Target{Ref: again}, "left", 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Locate(ctx, Locator{Role: "button", Name: "Delete", Nth: 0, Of: 3}); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("a locator of three: %v, want ErrAmbiguous", err)
	}
	if _, err := b.Locate(ctx, Locator{Role: "button", Name: "Nothing"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a locator of none: %v, want ErrNotFound", err)
	}
	if alerts, err := b.Alerts(ctx); err != nil || len(alerts) != 1 || alerts[0] != "Saved 3 notes" {
		t.Errorf("alerts %q, %v", alerts, err)
	}
}

func TestBrowser_KeysAndTheWheel(t *testing.T) {
	b := startBrowser(t, Options{})
	s := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := b.Navigate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	read, err := b.Read(ctx, "interactive", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	email := refOf(t, read, `textbox "Email"`)
	if err := b.Click(ctx, Target{Ref: email}, "", 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Type(ctx, "abcd"); err != nil {
		t.Fatal(err)
	}
	// abcd| → abc|d → ab|d, then a|bd → |bd.
	if err := b.Key(ctx, "left Backspace", 2); err != nil {
		t.Fatal(err)
	}
	if read, err := b.Read(ctx, "", 0, email); err != nil || !strings.Contains(read, `value="bd"`) {
		t.Errorf("the field reads %q, %v; want bd", read, err)
	}
	if err := b.Scroll(ctx, 100, 100, "down", 1); err != nil {
		t.Fatal(err)
	}
	if err := b.Hover(ctx, Target{Ref: email}); err != nil {
		t.Fatal(err)
	}
}

func TestBrowser_StartsWithAStorageState(t *testing.T) {
	s := server(t)
	state := filepath.Join(t.TempDir(), "state.json")
	host := strings.TrimPrefix(s.URL, "http://")
	host = host[:strings.LastIndex(host, ":")]
	if err := os.WriteFile(state, []byte(`{"cookies":[{"name":"sid","value":"s-7","domain":"`+host+`","path":"/","expires":-1,"httpOnly":true,"secure":false,"sameSite":"Lax"}],"origins":[{"origin":"`+s.URL+`","localStorage":[{"name":"token","value":"t-42"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b := startBrowser(t, Options{StorageState: state})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := b.Navigate(ctx, s.URL+"/whoami"); err != nil {
		t.Fatal(err)
	}
	if _, title, err := b.State(ctx); err != nil || title != "s-7" {
		t.Errorf("the server saw %q, %v; want the cookie of the state", title, err)
	}
	// The page opened to write the localStorage is answered inside the
	// browser, and is no request of the test.
	for _, r := range b.Requests().Slowest {
		if !strings.HasSuffix(r.URL, "/whoami") {
			t.Errorf("a request of the storage state was recorded: %+v", b.Requests())
		}
	}
}

func TestSlowest(t *testing.T) {
	var list []Request
	for _, d := range []int64{5, 30, 10, 30, 1, 20} {
		list = slowest(list, Request{DurationMS: d}, 4)
	}
	var got []int64
	for _, r := range list {
		got = append(got, r.DurationMS)
	}
	if fmt.Sprint(got) != "[30 30 20 10]" {
		t.Errorf("slowest %v", got)
	}
}

// TestWithoutPageAtFailure leaves the page at the failure out of an error
// of probe-browser, and keeps the error it was, as a refusal of the guard.
func TestWithoutPageAtFailure(t *testing.T) {
	refused := &actionrpc.Refused{Reason: "not allowed"}
	err := withoutPageAtFailure(fmt.Errorf("click #go: %w (page at failure: url http://a/, screenshot s.png, html s.html)", refused))
	if err.Error() != "click #go: "+refused.Error() {
		t.Errorf("error %q", err)
	}
	var got *actionrpc.Refused
	if !errors.As(err, &got) {
		t.Errorf("%v is no longer the refusal", err)
	}
	plain := errors.New("plain")
	if withoutPageAtFailure(plain) != plain {
		t.Error("an error with no page at the failure is changed")
	}
}
