package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// The newsletter of Acme Notes, which mails a link to confirm a
// subscription, and the mailbox the mails go to, read over JMAP at
// /.well-known/jmap with the user and the password of mailUser and
// mailPassword. The mailbox holds every mail the server sends, to anyone,
// as a mail catcher of a staging server does. It answers Email/query and
// Email/get, the methods that read, and no other.

const (
	mailUser     = "tester"
	mailPassword = "e2e-mail"
	mailAccount  = "a1"
)

// sentMail is a mail the server sent.
type sentMail struct {
	ID       string
	To       string
	Subject  string
	Received time.Time
	Text     string
	HTML     string
}

// newsletter keeps the subscriptions and the mailbox.
type newsletter struct {
	mu      sync.Mutex
	mails   []sentMail
	pending map[string]string
	subs    map[string]bool
}

func newNewsletter() *newsletter {
	return &newsletter{pending: map[string]string{}, subs: map[string]bool{}}
}

func (n *newsletter) handle(mux *http.ServeMux) {
	mux.HandleFunc("GET /newsletter", n.form)
	mux.HandleFunc("POST /newsletter", n.subscribe)
	mux.HandleFunc("GET /newsletter/confirm", n.confirm)
	mux.HandleFunc("GET /.well-known/jmap", n.authorized(n.session))
	mux.HandleFunc("POST /jmap/api", n.authorized(n.api))
}

// newsletterPage writes a page of the newsletter with body.
func newsletterPage(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>%s - Acme Notes</title></head>
<body><header><nav><a href="/">Acme Notes</a></nav></header><main>
%s
</main></body></html>
`, html.EscapeString(title), body)
}

func (n *newsletter) form(w http.ResponseWriter, r *http.Request) {
	newsletterPage(w, 200, "Newsletter", `<h1>The Acme Notes newsletter</h1>
<p>Tips on keeping notes, once a month.</p>
<form method="post" action="/newsletter">
<label for="email">Email address</label>
<input id="email" name="email" type="email" required>
<button type="submit">Subscribe</button>
</form>`)
}

// subscribe mails a link to confirm the subscription of an address.
func (n *newsletter) subscribe(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	if !strings.Contains(email, "@") {
		newsletterPage(w, 422, "Newsletter", `<h1>The Acme Notes newsletter</h1><p role="alert">Enter your email address.</p><p><a href="/newsletter">Back</a></p>`)
		return
	}
	b := make([]byte, 12)
	rand.Read(b)
	token := hex.EncodeToString(b)
	link := "http://" + r.Host + "/newsletter/confirm?token=" + token
	n.mu.Lock()
	n.pending[token] = email
	n.mails = append(n.mails, sentMail{
		ID:       fmt.Sprintf("m%d", len(n.mails)+1),
		To:       email,
		Subject:  "Confirm your subscription to Acme Notes",
		Received: time.Now().UTC(),
		Text:     "Hello,\n\nConfirm your subscription to the Acme Notes newsletter by opening this link:\n\n" + link + "\n\nIf you did not ask for it, ignore this mail.\n",
		HTML:     `<p>Hello,</p><p>Confirm your subscription to the Acme Notes newsletter: <a href="` + link + `">Confirm my subscription</a></p><p>If you did not ask for it, ignore this mail.</p>`,
	})
	n.mu.Unlock()
	log.Printf("newsletter: mailed %s a link to confirm", email)
	newsletterPage(w, 200, "Check your inbox", `<h1>Check your inbox</h1><p>We sent a link to `+html.EscapeString(email)+`. Open it to confirm your subscription.</p>`)
}

// confirm confirms the subscription the token of a link names.
func (n *newsletter) confirm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	n.mu.Lock()
	email, ok := n.pending[token]
	if ok {
		delete(n.pending, token)
		n.subs[email] = true
	}
	n.mu.Unlock()
	if !ok {
		newsletterPage(w, 404, "Link expired", `<h1>This link has expired</h1><p><a href="/newsletter">Subscribe again</a></p>`)
		return
	}
	log.Printf("newsletter: %s confirmed", email)
	newsletterPage(w, 200, "Subscribed", `<h1>You are subscribed</h1><p role="status">The subscription of `+html.EscapeString(email)+` is confirmed.</p>`)
}

// authorized lets in the user of the mailbox alone.
func (n *newsletter) authorized(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != mailUser || pass != mailPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="mail"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

// session is the JMAP session of the mailbox.
func (n *newsletter) session(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"capabilities": map[string]any{
			"urn:ietf:params:jmap:core": map[string]any{
				"maxSizeUpload": 0, "maxConcurrentUpload": 1, "maxSizeRequest": 1 << 20, "maxConcurrentRequests": 4,
				"maxCallsInRequest": 16, "maxObjectsInGet": 256, "maxObjectsInSet": 0, "collationAlgorithms": []string{},
			},
			"urn:ietf:params:jmap:mail": map[string]any{},
		},
		"accounts": map[string]any{
			mailAccount: map[string]any{"name": mailUser, "isPersonal": true, "isReadOnly": true, "accountCapabilities": map[string]any{"urn:ietf:params:jmap:mail": map[string]any{}}},
		},
		"primaryAccounts": map[string]any{"urn:ietf:params:jmap:mail": mailAccount},
		"username":        mailUser,
		"apiUrl":          "/jmap/api",
		"downloadUrl":     "/jmap/download/{accountId}/{blobId}/{name}?accept={type}",
		"uploadUrl":       "/jmap/upload/{accountId}/",
		"eventSourceUrl":  "/jmap/events",
		"state":           "s1",
	})
}

// api answers the method calls of a JMAP request.
func (n *newsletter) api(w http.ResponseWriter, r *http.Request) {
	var raw struct {
		Using       []string            `json:"using"`
		MethodCalls [][]json.RawMessage `json:"methodCalls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"type": "urn:ietf:params:jmap:error:notJSON", "status": 400, "detail": err.Error()})
		return
	}
	var responses []any
	results := map[string]map[string]any{}
	for _, c := range raw.MethodCalls {
		var name, id string
		var args map[string]any
		if len(c) != 3 || json.Unmarshal(c[0], &name) != nil || json.Unmarshal(c[1], &args) != nil || json.Unmarshal(c[2], &id) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"type": "urn:ietf:params:jmap:error:notRequest", "status": 400})
			return
		}
		out, err := n.call(name, args, results)
		if err != "" {
			responses = append(responses, []any{"error", map[string]any{"type": err}, id})
			continue
		}
		results[id] = out
		responses = append(responses, []any{name, out, id})
	}
	writeJSON(w, http.StatusOK, map[string]any{"methodResponses": responses, "sessionState": "s1"})
}

// call runs a method, with the results of the earlier calls of the request
// for its back-references, and returns its result, or the type of its error.
func (n *newsletter) call(name string, args map[string]any, results map[string]map[string]any) (map[string]any, string) {
	if acc, ok := args["accountId"]; ok && acc != mailAccount {
		return nil, "accountNotFound"
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	switch name {
	case "Email/query":
		filter, _ := args["filter"].(map[string]any)
		var ids []string
		for i := len(n.mails) - 1; i >= 0; i-- {
			if m := n.mails[i]; m.matches(filter) {
				ids = append(ids, m.ID)
			}
		}
		total := len(ids)
		if limit, ok := args["limit"].(float64); ok && int(limit) < len(ids) {
			ids = ids[:int(limit)]
		}
		return map[string]any{"accountId": mailAccount, "queryState": fmt.Sprint(len(n.mails)), "canCalculateChanges": false, "position": 0, "ids": ids, "total": total}, ""
	case "Email/get":
		var ids []string
		if ref, ok := args["#ids"].(map[string]any); ok {
			from, _ := ref["resultOf"].(string)
			if ref["path"] != "/ids" || results[from] == nil {
				return nil, "invalidResultReference"
			}
			ids, _ = results[from]["ids"].([]string)
		} else {
			list, _ := args["ids"].([]any)
			for _, id := range list {
				s, _ := id.(string)
				ids = append(ids, s)
			}
		}
		var props []string
		if p, ok := args["properties"].([]any); ok {
			for _, v := range p {
				s, _ := v.(string)
				props = append(props, s)
			}
		}
		list, notFound := []any{}, []string{}
		for _, id := range ids {
			i := slices.IndexFunc(n.mails, func(m sentMail) bool { return m.ID == id })
			if i < 0 {
				notFound = append(notFound, id)
				continue
			}
			list = append(list, n.mails[i].jmap(props, args["fetchTextBodyValues"] == true, args["fetchHTMLBodyValues"] == true))
		}
		return map[string]any{"accountId": mailAccount, "state": fmt.Sprint(len(n.mails)), "list": list, "notFound": notFound}, ""
	}
	return nil, "unknownMethod"
}

// matches says whether m matches a FilterCondition of Email/query.
func (m sentMail) matches(filter map[string]any) bool {
	has := func(field, in string) bool {
		v, _ := filter[field].(string)
		return v == "" || strings.Contains(strings.ToLower(in), strings.ToLower(v))
	}
	if after, _ := filter["after"].(string); after != "" {
		t, err := time.Parse(time.RFC3339, after)
		if err != nil || !m.Received.After(t) {
			return false
		}
	}
	return has("from", newsletterFrom) && has("to", m.To) && has("subject", m.Subject) && has("text", m.Subject+"\n"+m.Text)
}

const newsletterFrom = "Acme Notes <no-reply@acme.test>"

// jmap is m as an Email object of JMAP, with props, or all of them when
// props is empty.
func (m sentMail) jmap(props []string, textValues, htmlValues bool) map[string]any {
	preview := strings.Join(strings.Fields(m.Text), " ")
	if len(preview) > 120 {
		preview = preview[:120]
	}
	all := map[string]any{
		"id":         m.ID,
		"from":       []any{map[string]any{"name": "Acme Notes", "email": "no-reply@acme.test"}},
		"to":         []any{map[string]any{"name": nil, "email": m.To}},
		"subject":    m.Subject,
		"receivedAt": m.Received.Format(time.RFC3339),
		"preview":    preview,
		"textBody":   []any{map[string]any{"partId": "1", "type": "text/plain"}},
		"htmlBody":   []any{map[string]any{"partId": "2", "type": "text/html"}},
	}
	values := map[string]any{}
	if textValues {
		values["1"] = map[string]any{"value": m.Text, "isEncodingProblem": false, "isTruncated": false}
	}
	if htmlValues {
		values["2"] = map[string]any{"value": m.HTML, "isEncodingProblem": false, "isTruncated": false}
	}
	all["bodyValues"] = values
	if len(props) == 0 {
		return all
	}
	out := map[string]any{"id": m.ID}
	for _, p := range props {
		if v, ok := all[p]; ok {
			out[p] = v
		}
	}
	return out
}
