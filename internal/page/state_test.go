package page

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mozership/probe-browser/storagestate"
)

// TestChrome_StartsWithAStorageState starts Chrome signed in: the server
// gets the cookie, HttpOnly and of the session as it is, and the page reads
// its localStorage. The page opened to write the localStorage reaches no
// server, and is not counted among the requests of the test.
func TestChrome_StartsWithAStorageState(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	var (
		mu    sync.Mutex
		paths []string
	)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		who := "nobody"
		if c, err := r.Cookie("sid"); err == nil {
			who = c.Value
		}
		fmt.Fprintf(w, `<!doctype html><title>Account</title><p id="server">server: %s</p><p id="token"></p>
<script>document.getElementById('token').textContent = 'token: ' + (localStorage.getItem('token') || 'none') + ', cookie: ' + (document.cookie || 'none');</script>`, who)
	}))
	defer s.Close()

	state := &storagestate.State{
		Cookies: []storagestate.Cookie{
			{Name: "sid", Value: "s-7", Domain: "127.0.0.1", Path: "/", Expires: -1, HTTPOnly: true, SameSite: "Lax"},
			{Name: "later", Value: "yes", Domain: "127.0.0.1", Path: "/", Expires: float64(time.Now().Add(time.Hour).Unix())},
		},
		Origins: []storagestate.Origin{
			{Origin: s.URL, LocalStorage: []storagestate.Item{{Name: "token", Value: "t-42"}}},
			{Origin: "http://other.test", LocalStorage: nil},
		},
	}
	c, err := Start(context.Background(), Options{Headless: true, Width: 1280, Height: 800, StorageState: state})
	if err != nil && strings.Contains(err.Error(), "starting Chrome") {
		t.Skipf("no Chrome to start: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if url, _, err := c.State(ctx); err != nil || url != "about:blank" {
		t.Errorf("starts on %q, %v; want about:blank", url, err)
	}
	if n := c.Requests().Count; n != 0 {
		t.Errorf("%d requests before the first page", n)
	}
	mu.Lock()
	if len(paths) != 0 {
		t.Errorf("the server was asked for %v before the first page", paths)
	}
	mu.Unlock()

	if err := c.Navigate(ctx, s.URL+"/account"); err != nil {
		t.Fatal(err)
	}
	text, err := c.Text(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"server: s-7", "token: t-42", "cookie: later=yes"} {
		if !strings.Contains(text, want) {
			t.Errorf("the page shows %q, want %q", text, want)
		}
	}
	// sid is HttpOnly, out of reach of the page's scripts.
	if strings.Contains(text, "sid=") {
		t.Errorf("the page reads the HttpOnly cookie: %q", text)
	}
}

func TestChrome_StartsWithABadStorageState(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	state := &storagestate.State{Cookies: []storagestate.Cookie{{Name: "sid", Value: "s-7"}}}
	c, err := Start(context.Background(), Options{Headless: true, Width: 1280, Height: 800, StorageState: state})
	if err == nil {
		c.Close()
		t.Fatal("a cookie of no domain was taken")
	}
	if strings.Contains(err.Error(), "starting Chrome") {
		t.Skipf("no Chrome to start: %v", err)
	}
	if !strings.Contains(err.Error(), "storage_state") {
		t.Errorf("error %v, want it to name storage_state", err)
	}
}

// TestStart_EndsWithItsContext stops a start whose context ends, as when
// Probe stops the action while the state loads, and keeps a browser that
// started running after its context ends.
func TestStart_EndsWithItsContext(t *testing.T) {
	startChrome(t) // skips where there is no Chrome
	state := &storagestate.State{Origins: []storagestate.Origin{{Origin: "http://app.test", LocalStorage: []storagestate.Item{{Name: "token", Value: "t-42"}}}}}

	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if c, err := Start(ended, Options{Headless: true, Width: 1280, Height: 800, StorageState: state}); err == nil {
		c.Close()
		t.Error("a start whose context ended went on")
	}

	ctx, cancel := context.WithCancel(context.Background())
	c, err := Start(ctx, Options{Headless: true, Width: 1280, Height: 800, StorageState: state})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cancel()
	actx, acancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer acancel()
	if url, _, err := c.State(actx); err != nil || url != "about:blank" {
		t.Errorf("after its context ended, the browser is on %q, %v", url, err)
	}
}
