package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGraphQLNotes(t *testing.T) {
	srv := httptest.NewServer(newMux())
	defer srv.Close()
	send := func(key, query string, vars map[string]any) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/graphql", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("X-Api-Key", key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, out
	}

	if code, _ := send("", "{ notes { id } }", nil); code != http.StatusUnauthorized {
		t.Errorf("without the key: %d", code)
	}
	_, out := send(apiKey, `mutation New($text: String!) { createNote(text: $text) { id text } }`, map[string]any{"text": "Buy milk"})
	if got, _ := json.Marshal(out["data"]); string(got) != `{"createNote":{"id":"q1","text":"Buy milk"}}` {
		t.Fatalf("a note written: %v", out)
	}
	_, out = send(apiKey, `mutation { createNote(text: " ") { id } }`, nil)
	if errs, _ := out["errors"].([]any); len(errs) != 1 || !strings.Contains(string(must(json.Marshal(errs))), "text is required") {
		t.Errorf("without a text: %v", out)
	}
	_, out = send(apiKey, `{ note(id: "q1") { text } missing: note(id: "q9") { text } notes { id } }`, nil)
	if got, _ := json.Marshal(out["data"]); string(got) != `{"missing":null,"note":{"text":"Buy milk"},"notes":[{"id":"q1"}]}` {
		t.Errorf("notes read: %s", got)
	}
	// The API answers introspection, by which the agent reads its schema.
	_, out = send(apiKey, `{ __schema { mutationType { name } } }`, nil)
	if got, _ := json.Marshal(out["data"]); string(got) != `{"__schema":{"mutationType":{"name":"Mutation"}}}` {
		t.Errorf("introspection: %s", got)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
