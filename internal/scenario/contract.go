package scenario

import (
	"fmt"
	"slices"
	"strings"
)

// Violation is what a contract of an API, its OpenAPI document or its
// .proto files, does not allow in a request or a response.
type Violation struct {
	// In is request or response.
	In      string `json:"in"`
	Field   string `json:"field,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message"`
}

// contractBreach is a violation of a contract by a step of the tester.
type contractBreach struct {
	Violation
	step int
	// call names the call in the record, such as POST https://api.test/v1/orders.
	call string
	// url is where the call went, for a finding; empty for one that has no
	// URL, as a gRPC call.
	url string
	// operation is what the contract matched the call to, such as
	// GET /orders/{id}; empty when it matched it to none.
	operation string
	// spec names the contract, such as the OpenAPI document.
	spec string
}

// violationLine is a violation in one line, as the tester, the judge and
// the reviewer read it.
func violationLine(v Violation) string {
	s := "in the " + v.In
	if v.Field != "" {
		s += ", " + v.Field
	}
	return s + ": " + violationText(v)
}

// violationText is the message of a violation, with its reason, which
// tells what the message may not, as got string, want integer.
func violationText(v Violation) string {
	if v.Reason == "" || strings.Contains(v.Message, v.Reason) {
		return v.Message
	}
	return v.Message + " (" + v.Reason + ")"
}

// contractFindings are the findings of the responses a contract does not
// allow: a deterministic check found them, so they need no evidence of the
// reviewer. A request that breaks it is the tester's to mend, not a
// finding, and so is a call the contract has no operation for, as a path
// the tester got wrong. The violations of one field of one operation are
// one finding.
func (l *ledger) contractFindings() []Finding {
	var out []Finding
	index := map[string]int{}
	for _, c := range l.breaches {
		if c.In != "response" || c.operation == "" {
			continue
		}
		op := c.operation
		key := op + "\x00" + c.Field
		i, ok := index[key]
		if !ok {
			title := "The response of " + op + " breaks " + c.spec
			if c.Field != "" {
				title += " at " + c.Field
			}
			title = l.secrets.mask(title)
			out = append(out, Finding{
				Title:    title,
				Severity: "medium",
				Category: "error",
				Expected: "A response " + c.spec + " allows.",
				Steps:    []int{},
				URL:      c.url,
				Verified: true,
			})
			i = len(out) - 1
			index[key] = i
		}
		f := &out[i]
		msg := l.secrets.mask(violationText(c.Violation))
		if !slices.ContainsFunc(f.Evidence, func(e Evidence) bool { return e.Quote == msg }) {
			f.Evidence = append(f.Evidence, Evidence{Quote: msg, Verified: true})
			if f.Observed != "" {
				f.Observed += " "
			}
			f.Observed += msg
		}
		if !slices.Contains(f.Steps, c.step) {
			f.Steps = append(f.Steps, c.step)
		}
	}
	return out
}

// breachLines are the violations of the contracts, as the judge and the
// reviewer are shown them.
func (l *ledger) breachLines() []string {
	var lines []string
	for _, c := range l.breaches {
		lines = append(lines, fmt.Sprintf("- step %d, %s: %s", c.step, c.call, l.secrets.mask(violationLine(c.Violation))))
	}
	return lines
}
