package scenario

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mozership/probe-agent/internal/llm"
)

// fakeMail is a mailbox whose mails come as the searches ask: a search
// finds the mails of arrivals that came by the time it is made, counted in
// searches, and records the queries.
type fakeMail struct {
	mails    []MailMessage
	arrivals []int
	searches []MailQuery
	reads    []string
	err      error
}

func (f *fakeMail) target() *Mail {
	return &Mail{URL: "https://mail.test", Search: f.search, Read: f.read}
}

func (f *fakeMail) search(_ context.Context, q MailQuery) (MailList, error) {
	f.searches = append(f.searches, q)
	if f.err != nil {
		return MailList{}, f.err
	}
	var list MailList
	for i := len(f.mails) - 1; i >= 0; i-- {
		m := f.mails[i]
		if i < len(f.arrivals) && f.arrivals[i] > len(f.searches) {
			continue
		}
		if q.Subject != "" && !strings.Contains(m.Subject, q.Subject) || q.To != "" && !strings.Contains(strings.Join(m.To, ","), q.To) {
			continue
		}
		if !q.After.IsZero() && !m.Received.After(q.After) {
			continue
		}
		list.Total++
		if q.Limit == 0 || len(list.Mails) < q.Limit {
			list.Mails = append(list.Mails, m.MailSummary)
		}
	}
	return list, nil
}

func (f *fakeMail) read(_ context.Context, id string) (MailMessage, error) {
	f.reads = append(f.reads, id)
	for _, m := range f.mails {
		if m.ID == id {
			return m, nil
		}
	}
	return MailMessage{}, errors.New("no mail " + id)
}

func TestRun_ReadsAMailbox(t *testing.T) {
	old := mailPoll
	mailPoll = time.Millisecond
	defer func() { mailPoll = old }()

	past := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	box := &fakeMail{
		mails: []MailMessage{
			{MailSummary: MailSummary{ID: "m1", From: []string{"news@acme.test"}, To: []string{"bob@example.com"}, Subject: "Old news", Received: past}},
			{
				MailSummary: MailSummary{ID: "m2", From: []string{"Acme <no-reply@acme.test>"}, To: []string{"alice@example.com"}, Subject: "Confirm your address", Received: time.Now().Add(time.Hour), Preview: "Hello,\n  open the link"},
				HTML:        `<html><head><style>p{}</style></head><body><p>Hello Alice,</p><p>Your code is hunter2-long. <a href="https://app.test/confirm?t=1">Confirm</a></p></body></html>`,
			},
		},
		// The mail to confirm comes on the third search: the one of Open,
		// and two of the tester's, the second of which waits for it.
		arrivals: []int{0, 4},
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "mail_search", map[string]any{"to": "{secret:EMAIL}", "new_only": true}),
			toolCall("2", "mail_search", map[string]any{"to": "{secret:EMAIL}", "subject": "Confirm", "new_only": true, "wait": 30}),
			toolCall("3", "mail_read", map[string]any{"id": "m2"}),
			toolCall("4", "finish", map[string]any{"status": "done", "summary": "Read."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{"https://app.test/confirm?t=1", `"Old news" from news@acme.test`}})},
	}}
	cfg := apiConfig()
	cfg.Secrets["PASSWORD"] = "hunter2-long"
	o, err := Run(t.Context(), cfg, Targets{Mail: box.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass {
		t.Fatalf("verdict %s: %s", o.Verdict, o.Reason)
	}
	for _, e := range o.Evidence {
		if !e.Verified {
			t.Errorf("evidence %q is not verified", e.Quote)
		}
	}
	// Open, the first search, and the second, which asks until the mail
	// comes.
	if len(box.searches) != 4 {
		t.Fatalf("searches %+v", box.searches)
	}
	q := box.searches[1]
	if q.To != "alice@example.com" || q.After.IsZero() || q.Limit != 20 {
		t.Errorf("the search %+v", q)
	}
	if box.searches[0].Limit != 1 || !box.searches[0].After.IsZero() {
		t.Errorf("the search of Open %+v", box.searches[0])
	}

	tester := m.chats[0]
	if !strings.Contains(tester.opts.System, "The mailbox at https://mail.test, which you read through the tools that start with mail_") {
		t.Errorf("system prompt:\n%s", tester.opts.System)
	}
	if !strings.HasSuffix(tester.opts.Prompt, `The mailbox at https://mail.test holds 1 mail; the latest, "Old news" from news@acme.test, came at 2026-01-02T03:04:05Z.`) || !strings.Contains(tester.opts.Prompt, "seen in the mailbox") {
		t.Errorf("prompt:\n%s", tester.opts.Prompt)
	}
	if got := tester.turns[1].Results[0].Text; got != "No mail matches." {
		t.Errorf("the first search: %q", got)
	}
	found := tester.turns[2].Results[0].Text
	if !strings.HasPrefix(found, "1 mail matches:\n- id=m2 at ") || !strings.HasSuffix(found, `from Acme <no-reply@acme.test> to {secret:EMAIL}: "Confirm your address" — Hello, open the link`) {
		t.Errorf("the second search: %q", found)
	}
	read := tester.turns[3].Results[0].Text
	want := "Subject: Confirm your address\n"
	if !strings.Contains(read, want) || !strings.Contains(read, "\n\nHello Alice,\nYour code is {secret:PASSWORD}. Confirm\n\nLinks:\n- https://app.test/confirm?t=1") || strings.Contains(read, "p{}") {
		t.Errorf("the mail:\n%s", read)
	}
	if o.Steps[2].Target != "mail" || o.Steps[2].URL != "https://mail.test" || o.Steps[2].Input["id"] != "m2" {
		t.Errorf("steps %+v", o.Steps)
	}
	state := TargetState{Kind: "mail", URL: "https://mail.test", Response: read}
	if len(o.Targets) != 1 || o.Targets[0] != state {
		t.Errorf("targets %+v", o.Targets)
	}
	if judge := m.chats[1].opts.Prompt; !strings.Contains(judge, "What the tester last read of the mailbox https://mail.test:\n<last_read>\nFrom: Acme") {
		t.Errorf("judge prompt:\n%s", judge)
	}
}

func TestMail_AMailboxThatCannotBeRead(t *testing.T) {
	box := &fakeMail{err: errors.New("401 Unauthorized")}
	d := newMailDriver(newLedger(config()), *box.target())
	if err := d.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := d.Opening(); got != "The mailbox at https://mail.test could not be read: 401 Unauthorized." {
		t.Errorf("opening %q", got)
	}
	res, step := d.Do(t.Context(), llm.Call{ID: "1", Name: "mail_search", Input: []byte(`{}`)})
	if !res.IsError || step.Error != "Error: 401 Unauthorized" {
		t.Errorf("search %+v %+v", res, step)
	}
	res, _ = d.Do(t.Context(), llm.Call{ID: "2", Name: "mail_read", Input: []byte(`{"id":" "}`)})
	if !res.IsError || !strings.Contains(res.Text, "needs the id") {
		t.Errorf("read %+v", res)
	}
	if o := d.Final(t.Context(), false); o.sections[0].text != "The tester read no mail." {
		t.Errorf("final %+v", o)
	}
}

func TestMail_AnEmptyMailboxAndASearchThatTimesOut(t *testing.T) {
	old := mailPoll
	mailPoll = time.Millisecond
	defer func() { mailPoll = old }()
	box := &fakeMail{}
	d := newMailDriver(newLedger(config()), *box.target())
	if err := d.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := d.Opening(); got != "The mailbox at https://mail.test is empty." {
		t.Errorf("opening %q", got)
	}
	// The test runs out of time while the search waits.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	res, _ := d.Do(ctx, llm.Call{ID: "1", Name: "mail_search", Input: []byte(`{"wait": 60}`)})
	if res.IsError || res.Text != "No mail matches, after waiting 1m0s." {
		t.Errorf("search %+v", res)
	}
}

func TestMailLinks(t *testing.T) {
	m := MailMessage{
		Text: "Open https://app.test/a?x=1. Or (https://app.test/b), or ftp://no.test/c, or https://app.test/a?x=1 again.",
		HTML: `<p><a href="https://app.test/b">b</a> <a href="mailto:x@y">mail</a> <a href="/relative">r</a> <A HREF="https://app.test/d">d</A></p>`,
	}
	want := []string{"https://app.test/b", "https://app.test/d", "https://app.test/a?x=1"}
	if got := mailLinks(m); !reflect.DeepEqual(got, want) {
		t.Errorf("links %q, want %q", got, want)
	}
}

func TestHTMLText(t *testing.T) {
	got := htmlText(`<html><head><title>T</title></head><body><script>x()</script><h1>Hi&amp;bye</h1><table><tr><td>a</td><td>b</td></tr></table>line<br>next</body></html>`)
	if want := "Hi&bye\na b\nline\nnext"; got != want {
		t.Errorf("text %q, want %q", got, want)
	}
}
