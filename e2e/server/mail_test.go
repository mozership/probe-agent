package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNewsletter(t *testing.T) {
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "e2e.sqlite"))
	srv := httptest.NewServer(newMux())
	status := func(email string) string {
		t.Helper()
		db, err := sql.Open("sqlite", os.Getenv("DB_PATH"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var s string
		var confirmed sql.NullString
		if err := db.QueryRow(`SELECT status, confirmed_at FROM subscriptions WHERE email = ?`, email).Scan(&s, &confirmed); err != nil {
			return err.Error()
		}
		return fmt.Sprintf("%s %v", s, confirmed.Valid)
	}
	defer srv.Close()
	jmap := func(auth bool, calls ...any) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"using": []string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"}, "methodCalls": calls})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/jmap/api", strings.NewReader(string(body)))
		if auth {
			req.SetBasicAuth(mailUser, mailPassword)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	if code, _ := jmap(false); code != http.StatusUnauthorized {
		t.Errorf("without the password: %d", code)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/.well-known/jmap", nil)
	req.SetBasicAuth(mailUser, mailPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var session map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()
	if session["apiUrl"] != "/jmap/api" || session["primaryAccounts"].(map[string]any)["urn:ietf:params:jmap:mail"] != mailAccount {
		t.Errorf("session %v", session)
	}

	resp, err = http.PostForm(srv.URL+"/newsletter", url.Values{"email": {"Ann@Example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "We sent a link to Ann@Example.com") {
		t.Fatalf("the page after subscribing:\n%s", page)
	}
	if got := status("Ann@Example.com"); got != "pending false" {
		t.Errorf("the subscription after subscribing: %s", got)
	}

	search := []any{
		[]any{"Email/query", map[string]any{"accountId": mailAccount, "filter": map[string]any{"to": "Ann@", "subject": "confirm", "after": "2000-01-01T00:00:00Z"}, "limit": 5, "calculateTotal": true}, "q"},
		[]any{"Email/get", map[string]any{"accountId": mailAccount, "#ids": map[string]any{"resultOf": "q", "name": "Email/query", "path": "/ids"}, "properties": []any{"id", "subject", "from"}}, "g"},
	}
	_, out := jmap(true, search...)
	responses := out["methodResponses"].([]any)
	got := responses[1].([]any)[1].(map[string]any)["list"].([]any)
	if len(got) != 1 || got[0].(map[string]any)["subject"] != "Confirm your subscription to Acme Notes" || got[0].(map[string]any)["textBody"] != nil {
		t.Fatalf("the search: %v", out)
	}
	_, out = jmap(true, []any{"Email/query", map[string]any{"filter": map[string]any{"to": "bob@"}}, "q"})
	if ids := out["methodResponses"].([]any)[0].([]any)[1].(map[string]any)["ids"]; ids != nil && len(ids.([]any)) != 0 {
		t.Errorf("a search for another address: %v", out)
	}
	_, out = jmap(true, []any{"Email/get", map[string]any{"ids": []any{"m1", "m9"}, "fetchTextBodyValues": true}, "g"}, []any{"Email/set", map[string]any{}, "s"})
	responses = out["methodResponses"].([]any)
	mail := responses[0].([]any)[1].(map[string]any)
	text := mail["list"].([]any)[0].(map[string]any)["bodyValues"].(map[string]any)["1"].(map[string]any)["value"].(string)
	if nf := mail["notFound"].([]any); len(nf) != 1 || nf[0] != "m9" {
		t.Errorf("notFound %v", mail["notFound"])
	}
	if r := responses[1].([]any); r[0] != "error" || r[1].(map[string]any)["type"] != "unknownMethod" {
		t.Errorf("Email/set: %v", r)
	}

	link := regexp.MustCompile(`http://\S+`).FindString(text)
	resp, err = http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	page, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "The subscription of Ann@Example.com is confirmed.") {
		t.Errorf("the page of the link:\n%s", page)
	}
	if got := status("Ann@Example.com"); got != "confirmed true" {
		t.Errorf("the subscription after confirming: %s", got)
	}
	if resp, _ = http.Get(link); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the link again: %d", resp.StatusCode)
	}
}
