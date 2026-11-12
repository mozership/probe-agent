package page

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReadStorageState(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "state.json")
	// As the browser action and Playwright write it.
	if err := os.WriteFile(good, []byte(`{
  "cookies": [{"name": "sid", "value": "s-7", "domain": "localhost", "path": "/", "expires": -1, "httpOnly": true, "secure": false, "sameSite": "Lax"}],
  "origins": [{"origin": "http://localhost:8090", "localStorage": [{"name": "token", "value": "t-42"}]}]
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStorageState(good)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Cookies) != 1 || s.Cookies[0] != (StateCookie{Name: "sid", Value: "s-7", Domain: "localhost", Path: "/", Expires: -1, HTTPOnly: true, SameSite: "Lax"}) {
		t.Errorf("cookies %+v", s.Cookies)
	}
	if len(s.Origins) != 1 || s.Origins[0].Origin != "http://localhost:8090" || s.Origins[0].LocalStorage[0] != (StateItem{Name: "token", Value: "t-42"}) {
		t.Errorf("origins %+v", s.Origins)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("cookies"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, want string }{
		{filepath.Join(dir, "missing.json"), "storage_state: open"},
		{bad, "is not a storage state"},
	} {
		if _, err := ReadStorageState(tc.path); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want it to say %q", tc.path, err, tc.want)
		}
	}
}

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

	state := &StorageState{
		Cookies: []StateCookie{
			{Name: "sid", Value: "s-7", Domain: "127.0.0.1", Path: "/", Expires: -1, HTTPOnly: true, SameSite: "Lax"},
			{Name: "later", Value: "yes", Domain: "127.0.0.1", Path: "/", Expires: float64(time.Now().Add(time.Hour).Unix())},
		},
		Origins: []StateOrigin{
			{Origin: s.URL, LocalStorage: []StateItem{{Name: "token", Value: "t-42"}}},
			{Origin: "http://other.test", LocalStorage: nil},
		},
	}
	c, err := Start(Options{Headless: true, Width: 1280, Height: 800, StorageState: state})
	if err != nil {
		t.Skipf("no Chrome to start: %v", err)
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
	state := &StorageState{Cookies: []StateCookie{{Name: "sid", Value: "s-7"}}}
	c, err := Start(Options{Headless: true, Width: 1280, Height: 800, StorageState: state})
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
