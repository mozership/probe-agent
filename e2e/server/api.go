package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// apiKey is the key the notes API takes in X-Api-Key, which the e2e
// workflow fixes for the agent.
const apiKey = "e2e-key"

// notesAPI is a JSON API of notes, kept in memory, which e2e/api/openapi.yml
// describes. Its list of notes breaks the document on purpose, giving the
// count as a string, for a persona test to find.
type notesAPI struct {
	mu    sync.Mutex
	notes []note
}

type note struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func (a *notesAPI) handle(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/notes", a.authorized(a.create))
	mux.HandleFunc("GET /api/v1/notes", a.authorized(a.list))
	mux.HandleFunc("GET /api/v1/notes/{id}", a.authorized(a.get))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// authorized answers a request without the key with a 401.
func (a *notesAPI) authorized(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != apiKey {
			apiError(w, http.StatusUnauthorized, "a valid X-Api-Key is required")
			return
		}
		h(w, r)
	}
}

func (a *notesAPI) create(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Text) == "" {
		apiError(w, http.StatusBadRequest, `the body must be JSON with a text, such as {"text":"Buy milk"}`)
		return
	}
	a.mu.Lock()
	n := note{ID: "n" + strconv.Itoa(len(a.notes)+1), Text: strings.TrimSpace(in.Text)}
	a.notes = append(a.notes, n)
	a.mu.Unlock()
	writeJSON(w, http.StatusCreated, n)
}

func (a *notesAPI) get(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, n := range a.notes {
		if n.ID == r.PathValue("id") {
			writeJSON(w, http.StatusOK, n)
			return
		}
	}
	apiError(w, http.StatusNotFound, "no note is "+r.PathValue("id"))
}

// list gives the count as a string, where the document says an integer.
func (a *notesAPI) list(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	notes := append([]note{}, a.notes...)
	writeJSON(w, http.StatusOK, map[string]any{"notes": notes, "count": fmt.Sprint(len(notes))})
}
