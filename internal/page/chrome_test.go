package page

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
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
	mux.HandleFunc("/next", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Next</title><h1>Second page</h1>`)
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
