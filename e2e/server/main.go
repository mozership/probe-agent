// Command server is the application the e2e workflows test: a sign-up
// that keeps its accounts in memory. An email with +broken in it makes the
// sign-up fail with a server error, as a bug would, so that a workflow can
// check that a test of it fails.
//
// It listens on 127.0.0.1:18090, or on the address ADDR names.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

type account struct {
	Name, Email, Password string
}

type app struct {
	mu       sync.Mutex
	accounts map[string]account
	sessions map[string]string
}

var page = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Title}} - Acme Notes</title>
<style>
body { font-family: sans-serif; max-width: 32rem; margin: 2rem auto; }
label { display: block; margin-top: .75rem; }
input[type=text], input[type=email], input[type=password] { width: 100%; padding: .4rem; }
.error { color: #b00; border: 1px solid #b00; padding: .5rem; }
</style></head>
<body>
<header><nav><a href="/">Acme Notes</a> · <a href="/signup">Sign up</a></nav></header>
<main>
{{if eq .Page "home"}}
  <h1>Acme Notes</h1>
  <p>Keep your notes in one place.</p>
  <p><a href="/signup">Create a free account</a></p>
{{else if eq .Page "signup"}}
  <h1>Create your account</h1>
  {{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
  <form method="post" action="/signup">
    <label for="name">Full name</label>
    <input id="name" name="name" type="text" value="{{.Name}}" required>
    <label for="email">Email address</label>
    <input id="email" name="email" type="email" value="{{.Email}}" required>
    <label for="password">Password (8 characters or more)</label>
    <input id="password" name="password" type="password" required>
    <label for="confirm">Confirm password</label>
    <input id="confirm" name="confirm" type="password" required>
    <label><input name="terms" type="checkbox" value="yes"> I agree to the terms of service</label>
    <p><button type="submit">Create account</button></p>
  </form>
{{else if eq .Page "welcome"}}
  <h1>Welcome to Acme Notes, {{.Name}}!</h1>
  <p>Your account {{.Email}} is ready.</p>
{{end}}
</main></body></html>`))

type view struct {
	Page, Title, Error, Name, Email string
}

func (a *app) render(w http.ResponseWriter, status int, v view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := page.Execute(w, v); err != nil {
		log.Print(err)
	}
}

func (a *app) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	a.render(w, 200, view{Page: "home", Title: "Home"})
}

func (a *app) signup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		a.render(w, 200, view{Page: "signup", Title: "Sign up"})
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	v := view{Page: "signup", Title: "Sign up", Name: name, Email: email}

	if strings.Contains(email, "+broken") {
		log.Printf("sign-up of %s: failing as asked", email)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	a.mu.Lock()
	_, taken := a.accounts[email]
	a.mu.Unlock()
	switch {
	case name == "" || email == "":
		v.Error = "Enter your name and email address."
	case len(password) < 8:
		v.Error = "The password must be 8 characters or more."
	case password != r.FormValue("confirm"):
		v.Error = "The passwords do not match."
	case r.FormValue("terms") != "yes":
		v.Error = "You must agree to the terms of service."
	case taken:
		v.Error = "An account with this email address already exists."
	}
	if v.Error != "" {
		a.render(w, 422, v)
		return
	}

	b := make([]byte, 16)
	rand.Read(b)
	sid := hex.EncodeToString(b)
	a.mu.Lock()
	a.accounts[email] = account{Name: name, Email: email, Password: password}
	a.sessions[sid] = email
	a.mu.Unlock()
	log.Printf("sign-up of %s", email)
	http.SetCookie(w, &http.Cookie{Name: "sid", Value: sid, Path: "/", HttpOnly: true})
	http.Redirect(w, r, "/welcome", http.StatusSeeOther)
}

func (a *app) welcome(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("sid")
	a.mu.Lock()
	var acc account
	var ok bool
	if err == nil {
		acc, ok = a.accounts[a.sessions[c.Value]]
	}
	a.mu.Unlock()
	if !ok {
		http.Redirect(w, r, "/signup", http.StatusSeeOther)
		return
	}
	a.render(w, 200, view{Page: "welcome", Title: "Welcome", Name: acc.Name, Email: acc.Email})
}

// users answers whether an account exists, for a workflow to check what the
// pages said.
func (a *app) users(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(r.URL.Query().Get("email"))
	a.mu.Lock()
	acc, ok := a.accounts[email]
	a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"exists": ok, "name": acc.Name})
}

func newMux() *http.ServeMux {
	a := &app{accounts: map[string]account{}, sessions: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.home)
	mux.HandleFunc("/signup", a.signup)
	mux.HandleFunc("/welcome", a.welcome)
	mux.HandleFunc("/api/users", a.users)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	return mux
}

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:18090"
	}
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, newMux()))
}
