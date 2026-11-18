package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSignUp(t *testing.T) {
	s := httptest.NewServer(newMux())
	defer s.Close()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}

	form := url.Values{"name": {"Alice"}, "email": {"Alice@Example.com"}, "password": {"longenough"}, "confirm": {"longenough"}, "terms": {"yes"}}
	res, err := c.PostForm(s.URL+"/signup", form)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "Welcome to Acme Notes, Alice!") || !strings.Contains(string(body), `href="/notes/new"`) {
		t.Fatalf("%d %s", res.StatusCode, body)
	}

	res, err = c.Get(s.URL + "/api/users?email=alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	if got["exists"] != true || got["name"] != "Alice" {
		t.Errorf("/api/users = %v", got)
	}

	// The note page the welcome page links to fails.
	res, _ = c.Get(s.URL + "/notes/new")
	res.Body.Close()
	if res.StatusCode != 500 {
		t.Errorf("/notes/new got %d; want 500", res.StatusCode)
	}

	// The same email again is refused.
	res, _ = c.PostForm(s.URL+"/signup", form)
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 422 || !strings.Contains(string(body), "already exists") {
		t.Errorf("%d %s", res.StatusCode, body)
	}
}

func TestSignUp_Refusals(t *testing.T) {
	s := httptest.NewServer(newMux())
	defer s.Close()
	base := url.Values{"name": {"Bob"}, "email": {"bob@example.com"}, "password": {"longenough"}, "confirm": {"longenough"}, "terms": {"yes"}}
	for want, change := range map[string]func(url.Values){
		"8 characters or more": func(v url.Values) { v.Set("password", "short"); v.Set("confirm", "short") },
		"do not match":         func(v url.Values) { v.Set("confirm", "different1") },
		"terms of service":     func(v url.Values) { v.Del("terms") },
	} {
		v := url.Values{}
		for k, vs := range base {
			v[k] = append([]string{}, vs...)
		}
		change(v)
		res, err := http.PostForm(s.URL+"/signup", v)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 422 || !strings.Contains(string(body), want) {
			t.Errorf("want %q: %d %s", want, res.StatusCode, body)
		}
	}

	v := url.Values{}
	for k, vs := range base {
		v[k] = vs
	}
	v.Set("email", "bob+broken@example.com")
	res, _ := http.PostForm(s.URL+"/signup", v)
	res.Body.Close()
	if res.StatusCode != 500 {
		t.Errorf("a +broken email got %d; want 500", res.StatusCode)
	}
}

func TestAccount(t *testing.T) {
	s := httptest.NewServer(newMux())
	defer s.Close()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}

	// Signed out, the account page sends to the sign-up.
	res, err := c.Get(s.URL + "/account")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Request.URL.Path != "/signup" {
		t.Errorf("signed out, /account went to %s; want /signup", res.Request.URL.Path)
	}
	header(t, c, s.URL+"/", `<a href="/signup">Sign up</a>`, `href="/account"`)

	form := url.Values{"name": {"Dave"}, "email": {"dave@example.com"}, "password": {"longenough"}, "confirm": {"longenough"}, "terms": {"yes"}}
	res, err = c.PostForm(s.URL+"/signup", form)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	res, err = c.Get(s.URL + "/account")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "Signed in as Dave (dave@example.com)") {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	// Signed in, every page links to the account rather than the sign-up.
	for _, path := range []string{"/", "/signup", "/welcome", "/account"} {
		header(t, c, s.URL+path, `<a href="/account">Account</a>`, `href="/signup"`)
	}

	res, err = c.PostForm(s.URL+"/account", url.Values{"name": {" "}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 422 || !strings.Contains(string(body), "Enter a name.") {
		t.Errorf("an empty name: %d %s", res.StatusCode, body)
	}

	res, err = c.PostForm(s.URL+"/account", url.Values{"name": {"Dave Renamed"}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "Your name is saved.") || !strings.Contains(string(body), "Signed in as Dave Renamed") {
		t.Errorf("%d %s", res.StatusCode, body)
	}

	res, err = c.Get(s.URL + "/api/users?email=dave@example.com")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	if got["name"] != "Dave Renamed" {
		t.Errorf("/api/users = %v", got)
	}
}

// header checks that the header of the page at url has want and not
// unwanted.
func header(t *testing.T, c *http.Client, url, want, unwanted string) {
	t.Helper()
	res, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	_, rest, _ := strings.Cut(string(body), "<header>")
	h, _, _ := strings.Cut(rest, "</header>")
	if !strings.Contains(h, want) || strings.Contains(h, unwanted) {
		t.Errorf("the header of %s is %s; want %s and no %s", url, h, want, unwanted)
	}
}

func TestHelp(t *testing.T) {
	s := httptest.NewServer(newMux())
	defer s.Close()
	res, err := http.Get(s.URL + "/help")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "the section named Accessibility") {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}

func TestNotesAPI(t *testing.T) {
	s := httptest.NewServer(newMux())
	defer s.Close()
	do := func(method, path, key, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, s.URL+path, strings.NewReader(body))
		if key != "" {
			req.Header.Set("X-Api-Key", key)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var got map[string]any
		json.NewDecoder(res.Body).Decode(&got)
		return res.StatusCode, got
	}

	if code, got := do("GET", "/api/v1/notes", "", ""); code != 401 || got["error"] == nil {
		t.Errorf("without the key: %d %v", code, got)
	}
	if code, got := do("POST", "/api/v1/notes", apiKey, `{}`); code != 400 || got["error"] == nil {
		t.Errorf("without a text: %d %v", code, got)
	}
	if code, got := do("POST", "/api/v1/notes", apiKey, `{"text":"Buy milk"}`); code != 201 || got["id"] != "n1" || got["text"] != "Buy milk" {
		t.Errorf("a note written: %d %v", code, got)
	}
	if code, got := do("GET", "/api/v1/notes/n1", apiKey, ""); code != 200 || got["text"] != "Buy milk" {
		t.Errorf("a note read: %d %v", code, got)
	}
	if code, _ := do("GET", "/api/v1/notes/n9", apiKey, ""); code != 404 {
		t.Errorf("a note that is not there: %d", code)
	}
	// The count is a string, against the document.
	if code, got := do("GET", "/api/v1/notes", apiKey, ""); code != 200 || got["count"] != "1" {
		t.Errorf("the list: %d %v", code, got)
	}
}
