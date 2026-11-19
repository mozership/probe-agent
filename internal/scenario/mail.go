package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/mozership/probe-agent/internal/llm"
)

// Mail is a mailbox, which the tester reads and does not send from.
type Mail struct {
	// URL is of the server of the mailbox.
	URL string
	// Search lists the mails that match q, the latest first. A search that
	// gets no answer, as one the guard of the run refuses, is an error.
	Search func(ctx context.Context, q MailQuery) (MailList, error)
	// Read returns the mail of id.
	Read func(ctx context.Context, id string) (MailMessage, error)
}

// MailQuery is what a search matches: each field that is not empty, all
// of them together.
type MailQuery struct {
	From, To, Subject, Text string
	// After is the time a mail must have come after.
	After time.Time
	// Limit is how many mails to list.
	Limit int
}

// MailList is the answer to a search.
type MailList struct {
	// Total is how many mails match, of which Mails are the latest.
	Total int
	Mails []MailSummary
}

// MailSummary is a mail as a search lists it.
type MailSummary struct {
	ID       string
	From, To []string
	Subject  string
	Received time.Time
	// Preview is the start of the text of the mail.
	Preview string
}

// MailMessage is a mail as it is read.
type MailMessage struct {
	MailSummary
	// Text is its plain text, and HTML its HTML, either of which may be
	// empty.
	Text, HTML string
}

// mailPoll is how often a search that waits asks again.
var mailPoll = 2 * time.Second

// maxMailWait is the longest a search waits for a mail.
const maxMailWait = 2 * time.Minute

// mailDriver is a mailbox as a target.
type mailDriver struct {
	*ledger
	box Mail
	// shownURL is the server as the model and the record see it, with the
	// secrets in it masked.
	shownURL string
	// start is when the test started. A search for new mail is for those
	// after latest, the mail that came last as it started, by the clock of
	// the server, which may not be that of the agent.
	start  time.Time
	latest *MailSummary
	// opening is what the mailbox held as the test started.
	opening string
	// last is the last mail read, or else the last search, for the judge.
	last string
}

var _ Driver = (*mailDriver)(nil)

func newMailDriver(l *ledger, box Mail) *mailDriver {
	return &mailDriver{ledger: l, box: box, shownURL: l.secrets.mask(box.URL)}
}

// Kind is mail.
func (d *mailDriver) Kind() string { return "mail" }

// Open notes when the test starts and what the mailbox holds. A mailbox
// that cannot be read is told to the tester, who can search again, and is
// not fatal.
func (d *mailDriver) Open(ctx context.Context) error {
	d.start = time.Now()
	list, err := d.box.Search(ctx, MailQuery{Limit: 1})
	switch {
	case err != nil:
		d.opening = "The mailbox at " + d.shownURL + " could not be read: " + d.secrets.mask(err.Error()) + "."
	case len(list.Mails) == 0:
		d.opening = "The mailbox at " + d.shownURL + " is empty."
	default:
		m := list.Mails[0]
		d.latest = &m
		d.opening = d.secrets.mask(fmt.Sprintf("The mailbox at %s holds %s; the latest, %q from %s, came at %s.",
			d.shownURL, mails(list.Total), m.Subject, strings.Join(m.From, ", "), m.Received.UTC().Format(time.RFC3339)))
	}
	d.seen(d.opening)
	return nil
}

// Offer offers the tools of the mailbox.
func (d *mailDriver) Offer(opts *llm.ChatOptions) {
	opts.Tools = append(opts.Tools, mailTools...)
}

// Handles says whether call is of a tool of the mailbox.
func (d *mailDriver) Handles(call llm.Call) bool {
	return call.Toolset == "" && slices.ContainsFunc(mailTools, func(t llm.Tool) bool { return t.Name == call.Name })
}

// Brief tells the tester of the mailbox.
func (d *mailDriver) Brief() string {
	return `The mailbox at ` + d.shownURL + `, which you read through the tools that start with mail_, and cannot send from:
- mail_search lists the mails that match a sender, a recipient, a subject or a text, the latest first. With new_only it lists only those that came after the test started, and with wait it waits for one to come, as a mail the application sends after you act on another target.
- mail_read shows one mail by its id: its headers, its text and its links. Open a link on the browser, if the test has one, to follow it.`
}

// Opening says what the mailbox held as the test started.
func (d *mailDriver) Opening() string { return d.opening }

// mailInput is the input of any tool of the mailbox; each tool reads the
// fields it takes.
type mailInput struct {
	From    string  `json:"from"`
	To      string  `json:"to"`
	Subject string  `json:"subject"`
	Text    string  `json:"text"`
	NewOnly bool    `json:"new_only"`
	Wait    float64 `json:"wait"`
	ID      string  `json:"id"`
}

// Do runs a call of a tool of the mailbox.
func (d *mailDriver) Do(ctx context.Context, call llm.Call) (llm.Result, Step) {
	step := Step{Tool: call.Name, Target: d.Kind(), URL: d.shownURL}
	var raw map[string]any
	if json.Unmarshal(call.Input, &raw) == nil && len(raw) > 0 {
		step.Input, _ = d.secrets.maskAny(raw).(map[string]any)
	}
	res := llm.Result{CallID: call.ID}
	var in mailInput
	text, err := "", json.Unmarshal(call.Input, &in)
	if err == nil {
		switch call.Name {
		case "mail_search":
			text, err = d.search(ctx, in)
		case "mail_read":
			text, err = d.read(ctx, strings.TrimSpace(in.ID))
		default:
			err = fmt.Errorf("there is no tool %s", call.Name)
		}
	}
	if err != nil {
		res.Text = "Error: " + d.secrets.mask(err.Error())
		res.IsError = true
		step.Error = res.Text
		return res, step
	}
	text = truncate(d.secrets.mask(text), maxText)
	d.seen(text)
	d.last = text
	res.Text = text
	step.Result = summary(res.Text)
	return res, step
}

// search lists the mails in matches, waiting for one as long as in asks,
// within the time of the test.
func (d *mailDriver) search(ctx context.Context, in mailInput) (string, error) {
	q := MailQuery{Limit: 20}
	var err error
	for _, f := range []struct {
		dst *string
		v   string
	}{{&q.From, in.From}, {&q.To, in.To}, {&q.Subject, in.Subject}, {&q.Text, in.Text}} {
		if *f.dst, err = d.secrets.fill(strings.TrimSpace(f.v)); err != nil {
			return "", err
		}
	}
	newOnly := in.NewOnly && !d.start.IsZero()
	switch {
	case !newOnly:
	case d.latest != nil && !d.latest.Received.IsZero():
		// A time of JMAP is to the second, and after is strict, so the
		// search is from the second before the latest mail, which is then
		// left out.
		q.After = d.latest.Received.Add(-time.Second)
	case d.latest == nil && d.opening != "" && !strings.Contains(d.opening, "could not be read"):
		// The mailbox was empty: every mail is new.
	default:
		// The mailbox could not be read: the clock of the agent, with
		// room for that of the server to be behind.
		q.After = d.start.Add(-time.Minute)
	}
	wait := min(time.Duration(max(in.Wait, 0)*float64(time.Second)), maxMailWait)
	began := time.Now()
	deadline := began.Add(wait)
	for {
		list, err := d.box.Search(ctx, q)
		if err != nil {
			return "", err
		}
		if newOnly && d.latest != nil {
			list = d.withoutLatest(list)
		}
		if len(list.Mails) > 0 || !time.Now().Before(deadline) {
			return mailListText(list, wait > 0, time.Since(began)), nil
		}
		select {
		case <-ctx.Done():
			return mailListText(list, wait > 0, time.Since(began)), nil
		case <-time.After(min(mailPoll, time.Until(deadline))):
		}
	}
}

// withoutLatest leaves out of list the mail that came last as the test
// started.
func (d *mailDriver) withoutLatest(list MailList) MailList {
	i := slices.IndexFunc(list.Mails, func(m MailSummary) bool { return m.ID == d.latest.ID })
	if i < 0 {
		return list
	}
	list.Mails = slices.Delete(slices.Clone(list.Mails), i, i+1)
	list.Total--
	return list
}

// mailListText tells the mails of list, of a search that waited, if it
// was to wait, as long as took.
func mailListText(list MailList, waited bool, took time.Duration) string {
	if len(list.Mails) == 0 {
		s := "No mail matches"
		if waited {
			s += fmt.Sprintf(", after waiting %s", took.Round(time.Second))
		}
		return s + "."
	}
	var b strings.Builder
	if list.Total == 1 {
		b.WriteString("1 mail matches")
	} else {
		fmt.Fprintf(&b, "%d mails match", list.Total)
	}
	if list.Total > len(list.Mails) {
		fmt.Fprintf(&b, "; the latest %d", len(list.Mails))
	}
	b.WriteString(":")
	for _, m := range list.Mails {
		fmt.Fprintf(&b, "\n- id=%s at %s from %s to %s: %q", m.ID, m.Received.UTC().Format(time.RFC3339),
			strings.Join(m.From, ", "), strings.Join(m.To, ", "), m.Subject)
		if p := strings.Join(strings.Fields(m.Preview), " "); p != "" {
			b.WriteString(" — " + p)
		}
	}
	return b.String()
}

// mails counts n mails.
func mails(n int) string {
	if n == 1 {
		return "1 mail"
	}
	return fmt.Sprintf("%d mails", n)
}

// read shows the mail of id.
func (d *mailDriver) read(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("mail_read needs the id of a mail, as mail_search lists it")
	}
	m, err := d.box.Read(ctx, id)
	if err != nil {
		return "", err
	}
	return mailText(m), nil
}

// mailText tells a mail: its headers, its text, and its links.
func mailText(m MailMessage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\nTo: %s\nSubject: %s\n", strings.Join(m.From, ", "), strings.Join(m.To, ", "), m.Subject)
	if !m.Received.IsZero() {
		fmt.Fprintf(&b, "Date: %s\n", m.Received.UTC().Format(time.RFC3339))
	}
	b.WriteString("\n")
	text := strings.TrimSpace(m.Text)
	if text == "" && m.HTML != "" {
		text = htmlText(m.HTML)
	}
	if text == "" {
		text = "(The mail has no text.)"
	}
	b.WriteString(text)
	if links := mailLinks(m); len(links) > 0 {
		b.WriteString("\n\nLinks:")
		for _, l := range links {
			b.WriteString("\n- " + l)
		}
	}
	return b.String()
}

// urlPattern finds a URL in plain text, which may hold parentheses, as
// one of Wikipedia does.
var urlPattern = regexp.MustCompile(`https?://[^\s<>"'\[\]]+`)

// mailLinks lists the links of m, of its HTML and of its text, each once,
// in the order they come.
func mailLinks(m MailMessage) []string {
	var links []string
	add := func(l string) {
		l = strings.TrimRight(strings.TrimSpace(l), ".,;:!?")
		// A closing parenthesis with no opening one in the URL closes
		// the text around it.
		for strings.HasSuffix(l, ")") && strings.Count(l, "(") < strings.Count(l, ")") {
			l = strings.TrimRight(strings.TrimSuffix(l, ")"), ".,;:!?")
		}
		if u, err := url.Parse(l); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return
		}
		if !slices.Contains(links, l) {
			links = append(links, l)
		}
	}
	if m.HTML != "" {
		z := html.NewTokenizer(strings.NewReader(m.HTML))
		for {
			tt := z.Next()
			if tt == html.ErrorToken {
				break
			}
			if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
				continue
			}
			if name, hasAttr := z.TagName(); string(name) == "a" && hasAttr {
				for {
					k, v, more := z.TagAttr()
					if string(k) == "href" {
						add(string(v))
					}
					if !more {
						break
					}
				}
			}
		}
	}
	for _, l := range urlPattern.FindAllString(m.Text, -1) {
		add(l)
	}
	return links
}

// htmlText is the text of an HTML mail, a line for each block, without its
// styles and scripts.
func htmlText(s string) string {
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	skip := 0
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			var lines []string
			for line := range strings.Lines(b.String()) {
				if l := strings.Join(strings.Fields(line), " "); l != "" {
					lines = append(lines, l)
				}
			}
			return strings.Join(lines, "\n")
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "style", "script", "title":
				if tt == html.StartTagToken {
					skip++
				} else if tt == html.EndTagToken && skip > 0 {
					skip--
				}
			case "body":
				// What a head left open, as a title never closed, ends
				// here.
				skip = 0
			case "br", "p", "div", "tr", "li", "h1", "h2", "h3", "h4", "table":
				b.WriteString("\n")
			}
		case html.TextToken:
			if skip == 0 {
				b.WriteString(html.UnescapeString(string(z.Text())) + " ")
			}
		}
	}
}

// Final is the last mail the tester read, or the last search. The mailbox
// is not read again, as a mail may come in the meantime.
func (d *mailDriver) Final(context.Context, bool) observation {
	o := observation{
		heading: "What the tester last read of the mailbox " + d.shownURL + ":",
		state:   TargetState{Kind: d.Kind(), URL: d.shownURL},
	}
	text := "The tester read no mail."
	if d.last != "" {
		text = truncate(d.last, 20000)
		o.state.Response = text
	}
	o.sections = []section{{"last_read", text}}
	return o
}

var mailTools = []llm.Tool{
	{
		Name:        "mail_search",
		Description: "List the mails of the mailbox that match all the fields given, the latest first, with their ids, senders, recipients, subjects and the start of their text.",
		Properties: map[string]any{
			"from":     map[string]any{"type": "string", "description": "A part of the address or the name of the sender."},
			"to":       map[string]any{"type": "string", "description": "A part of the address or the name of a recipient."},
			"subject":  map[string]any{"type": "string", "description": "A part of the subject."},
			"text":     map[string]any{"type": "string", "description": "A word or a phrase of the mail."},
			"new_only": map[string]any{"type": "boolean", "description": "List only the mails that came after the test started."},
			"wait":     map[string]any{"type": "integer", "description": "Seconds to wait for a mail to match, up to 120, as for a mail the application sends after you act. 0 or left out does not wait."},
		},
	},
	{
		Name:        "mail_read",
		Description: "Read a mail: its headers, its text and its links.",
		Properties: map[string]any{
			"id": map[string]any{"type": "string", "description": "The id of the mail, as mail_search lists it."},
		},
		Required: []string{"id"},
	},
}
