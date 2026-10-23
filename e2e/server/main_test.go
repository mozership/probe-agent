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
