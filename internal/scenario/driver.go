package scenario

import (
	"context"

	"github.com/mozership/probe-agent/internal/llm"
)

// Driver is a target the tester uses, such as the browser: the tools it
// offers the model, and how it runs the calls of them. A test has a Driver
// for each target it names.
type Driver interface {
	// Kind names the target, such as browser.
	Kind() string
	// Open readies the target before the tester starts, as the browser
	// opens the first page.
	Open(ctx context.Context) error
	// Offer adds the tools of the target to the chat of the tester.
	Offer(opts *llm.ChatOptions)
	// Handles says whether call is a call of a tool of the target.
	Handles(call llm.Call) bool
	// Do runs a call of a tool of the target, and returns its result and
	// the step that records it, for Index and Notes to be filled in.
	Do(ctx context.Context, call llm.Call) (llm.Result, Step)
	// Brief tells the tester of a test of more than one target, or of
	// another target than the browser, what the target is and how to use
	// it, under a line naming it.
	Brief() string
	// Opening says what the target shows as the tester starts.
	Opening() string
	// Final is what the target shows once the tester is done, for the
	// judge or the reviewer, with vision whether the model sees images.
	Final(ctx context.Context, vision bool) observation
}

// ledger is what a test keeps of what the tester did and what its targets
// showed: the drivers add to it, and the judge and the reviewer read it.
type ledger struct {
	cfg     Config
	secrets secrets

	steps []Step
	// shown are the texts of the elements the targets showed the tester,
	// which a value it typed may have been read off.
	shown []string
	// texts are what the targets showed the tester, which the judge's
	// quotes are checked against.
	texts []string
}

func newLedger(cfg Config) *ledger {
	return &ledger{cfg: cfg, secrets: newSecrets(cfg.Secrets)}
}

// seen keeps text as a target showed it to the tester, masked as the model
// read it.
func (l *ledger) seen(text string) {
	masked := l.secrets.mask(text)
	l.texts = append(l.texts, masked)
	l.shown = append(l.shown, shownTexts(masked)...)
}

// save keeps an image as evidence under name, and returns where it was
// kept, or "" when it was not.
func (l *ledger) save(name string, data []byte) string {
	if l.cfg.Save == nil {
		return ""
	}
	return l.cfg.Save(name, data)
}
